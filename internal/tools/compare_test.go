package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaos/llm-eyes-mcp/internal/cache"
	"github.com/xiaos/llm-eyes-mcp/internal/imageio"
	"github.com/xiaos/llm-eyes-mcp/internal/vlm"
)

// Mocks answer exactly the ids the prompt asks about, so tests hold at any
// compareBatchSize: each batch sees a reply complete for its own regions.

// compareCanned is the detection a mock returns for one region id. found=false
// produces a degenerate [0,0,0,0] box, exercising the missing path.
type compareCanned struct {
	bbox  [4]float64 // normalised
	text  string
	found bool
}

func twoRegionCanned() map[string]compareCanned {
	return map[string]compareCanned{
		"btn":   {bbox: [4]float64{0.1, 0.8, 0.9, 0.95}, text: "开始匹配", found: true},
		"title": {bbox: [4]float64{0.1, 0.1, 0.9, 0.2}, text: "Reward Name", found: true},
	}
}

// compareOKMock returns perfect detections for every requested region.
func compareOKMock(name string) *vlm.Mock {
	return compareEchoMock(name, twoRegionCanned())
}

// compareMissingMock never locates title, exercising the missing path.
func compareMissingMock(name string) *vlm.Mock {
	canned := twoRegionCanned()
	canned["title"] = compareCanned{found: false}
	return compareEchoMock(name, canned)
}

// compareEchoMock replies with exactly one detection per id the prompt asks
// about, using per-id canned values.
func compareEchoMock(name string, canned map[string]compareCanned) *vlm.Mock {
	m := vlm.NewMock(name, "")
	m.CompleteFn = func(_ context.Context, req vlm.Request) (*vlm.Response, error) {
		ids := promptRegionIDs(req.Prompt)
		parts := make([]string, 0, len(ids))
		for _, id := range ids {
			if c, ok := canned[id]; ok && c.found {
				parts = append(parts, fmt.Sprintf(`{"id":%q,"bbox":[%.3f,%.3f,%.3f,%.3f],"text":%q}`,
					id, c.bbox[0], c.bbox[1], c.bbox[2], c.bbox[3], c.text))
				continue
			}
			parts = append(parts, fmt.Sprintf(`{"id":%q,"bbox":[0,0,0,0],"text":""}`, id))
		}
		return &vlm.Response{
			Text:  `{"regions":[` + strings.Join(parts, ",") + `]}`,
			Model: name + "-mock",
		}, nil
	}
	return m
}

// promptRegionIDs extracts the region ids a compare prompt asks about, from
// either the first-round bullet list ("- id=X") or the retry prompt's missing
// list ("missing or malformed: a, b").
func promptRegionIDs(prompt string) []string {
	var ids []string
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- id=") {
			continue
		}
		id := strings.TrimPrefix(line, "- id=")
		if i := strings.IndexAny(id, " \t"); i > 0 {
			id = id[:i]
		}
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		return ids
	}
	const marker = "missing or malformed: "
	i := strings.Index(prompt, marker)
	if i < 0 {
		return nil
	}
	rest := prompt[i+len(marker):]
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}
	rest = strings.TrimSuffix(strings.TrimSpace(rest), ".")
	for _, id := range strings.Split(rest, ", ") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// numBatches reports how many VLM calls n regions take at the current
// compareBatchSize, so assertions track the constant instead of hardcoding it.
func numBatches(n int) int { return (n + compareBatchSize - 1) / compareBatchSize }

// writeCompareTestImage: top 20% and bottom 20% are #FF6600, middle black.
func writeCompareTestImage(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{R: 0, G: 0, B: 0, A: 255}
			if y < h/5 || y >= h*4/5 {
				c = color.RGBA{R: 255, G: 102, B: 0, A: 255}
			}
			img.Set(x, y, c)
		}
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "compare.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return p
}

func writeCompareSpecFile(t *testing.T, spec map[string]any) string {
	t.Helper()
	blob, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(p, blob, 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	return p
}

// twoRegionSpec is the baseline spec matching both OK and partial mocks.
func twoRegionSpec(t *testing.T, extra map[string]any) map[string]any {
	regions := []map[string]any{
		{"id": "btn", "desc": "bottom button", "bbox": []float64{37, 650, 338, 772}},
		{"id": "title", "desc": "top title", "bbox": []float64{37, 81, 338, 162}},
	}
	out := map[string]any{"version": 1, "regions": regions}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func newCompareDeps(t *testing.T, mock *vlm.Mock) (*Deps, *vlm.Mock) {
	t.Helper()
	mgr, err := cache.Open(cache.Settings{Root: t.TempDir(), L3MaxEntries: 64})
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	loader := imageio.NewLoader(30 * time.Second)
	loader.Archive = mgr.ArchiveLookup()
	deps := &Deps{
		Loader:    loader,
		Cache:     mgr,
		Providers: vlm.NewSet("qwen", mock),
		MaxRounds: 2,
		Logf:      t.Logf,
	}
	return deps, mock
}

func TestCompareDesignFullMatch(t *testing.T) {
	deps, mock := newCompareDeps(t, compareOKMock("qwen"))
	tool := NewCompareTool(deps)
	p := writeCompareTestImage(t, 375, 812)

	res := call(t, tool, map[string]any{
		"image_source": p,
		"spec":         writeCompareSpecFile(t, twoRegionSpec(t, nil)),
	})
	if res == nil {
		t.Fatal("compare returned nil")
	}
	if res.IsError {
		t.Fatalf("compare should succeed, got: %s", resultText(res))
	}
	if mock.CallCount() != numBatches(2) {
		t.Errorf("first call must invoke VLM once per batch, got count=%d want %d", mock.CallCount(), numBatches(2))
	}
	txt := resultText(res)
	if !strings.Contains(txt, "score=") {
		t.Errorf("result must include score: %s", txt)
	}
	if !strings.Contains(txt, "2/2 regions ok") && !strings.Contains(txt, "All 2 regions match") {
		t.Errorf("result should indicate all regions ok: %s", txt)
	}
}

func TestCompareDesignColorMismatch(t *testing.T) {
	deps, _ := newCompareDeps(t, compareOKMock("qwen"))
	tool := NewCompareTool(deps)
	p := writeCompareTestImage(t, 375, 812)
	// Spec expects blue for btn but actual is #FF6600.
	spec := twoRegionSpec(t, nil)
	spec["regions"].([]map[string]any)[0]["color"] = "#0000FF"
	spec["regions"].([]map[string]any)[1]["color"] = "#FF6600"

	res := call(t, tool, map[string]any{"image_source": p, "spec": writeCompareSpecFile(t, spec)})
	if res.IsError {
		t.Fatalf("compare should succeed: %s", resultText(res))
	}
	txt := resultText(res)
	if !strings.Contains(txt, "color_mismatch") {
		t.Errorf("result should flag color mismatch: %s", txt)
	}
}

func TestCompareDesignTextMismatch(t *testing.T) {
	deps, _ := newCompareDeps(t, compareOKMock("qwen"))
	tool := NewCompareTool(deps)
	p := writeCompareTestImage(t, 375, 812)
	spec := twoRegionSpec(t, nil)
	spec["regions"].([]map[string]any)[0]["text"] = "完全不同的文字"
	spec["regions"].([]map[string]any)[1]["text"] = "Reward Name"

	res := call(t, tool, map[string]any{"image_source": p, "spec": writeCompareSpecFile(t, spec)})
	if res.IsError {
		t.Fatalf("compare should succeed: %s", resultText(res))
	}
	txt := resultText(res)
	if !strings.Contains(txt, "text_mismatch") {
		t.Errorf("result should flag text mismatch: %s", txt)
	}
}

func TestCompareDesignMissingRegion(t *testing.T) {
	deps, _ := newCompareDeps(t, compareMissingMock("qwen"))
	tool := NewCompareTool(deps)
	p := writeCompareTestImage(t, 375, 812)

	res := call(t, tool, map[string]any{"image_source": p, "spec": writeCompareSpecFile(t, twoRegionSpec(t, nil))})
	if res.IsError {
		t.Fatalf("compare should succeed even with missing regions: %s", resultText(res))
	}
	txt := resultText(res)
	if !strings.Contains(txt, "missing") {
		t.Errorf("result should flag missing region: %s", txt)
	}
	if !strings.Contains(txt, "1/2") {
		t.Errorf("score should reflect 1 of 2 ok: %s", txt)
	}
}

func TestCompareDesignCachesVLMLocate(t *testing.T) {
	deps, mock := newCompareDeps(t, compareOKMock("qwen"))
	tool := NewCompareTool(deps)
	p := writeCompareTestImage(t, 375, 812)
	spec := writeCompareSpecFile(t, twoRegionSpec(t, nil))

	args := map[string]any{"image_source": p, "spec": spec}
	if res := call(t, tool, args); res.IsError {
		t.Fatalf("first call failed: %s", resultText(res))
	}
	if mock.CallCount() != numBatches(2) {
		t.Fatalf("first call must invoke VLM once per batch, count=%d want %d", mock.CallCount(), numBatches(2))
	}
	res2 := call(t, tool, args)
	if res2.IsError {
		t.Fatalf("second call failed: %s", resultText(res2))
	}
	if mock.CallCount() != numBatches(2) {
		t.Errorf("repeat must be served from cache, VLM count=%d want %d", mock.CallCount(), numBatches(2))
	}
	txt := resultText(res2)
	if !strings.Contains(txt, "cache=L2") {
		t.Errorf("repeat result must report L2 tier: %s", txt)
	}
}

// TestCompareDesignPartialRetry drives one unparsable VLM reply through the
// retry path: the first round fails to parse, the retry converges, and the
// run still succeeds.
func TestCompareDesignPartialRetry(t *testing.T) {
	mock := compareEchoMock("qwen", twoRegionCanned())
	var mu sync.Mutex
	calls := 0
	orig := mock.CompleteFn
	mock.CompleteFn = func(ctx context.Context, req vlm.Request) (*vlm.Response, error) {
		mu.Lock()
		n := calls
		calls++
		mu.Unlock()
		if n == 0 {
			return &vlm.Response{Text: "not json at all", Model: "qwen-mock"}, nil
		}
		return orig(ctx, req)
	}
	deps, _ := newCompareDeps(t, mock)
	tool := NewCompareTool(deps)
	p := writeCompareTestImage(t, 375, 812)

	res := call(t, tool, map[string]any{"image_source": p, "spec": writeCompareSpecFile(t, twoRegionSpec(t, nil))})
	if res.IsError {
		t.Fatalf("compare should succeed after retry: %s", resultText(res))
	}
	if want := numBatches(2) + 1; calls != want {
		t.Errorf("one failed round should cost exactly one retry, VLM count=%d want %d", calls, want)
	}
}

func TestCompareDesignRefreshBypassesCache(t *testing.T) {
	deps, mock := newCompareDeps(t, compareOKMock("qwen"))
	tool := NewCompareTool(deps)
	p := writeCompareTestImage(t, 375, 812)
	spec := writeCompareSpecFile(t, twoRegionSpec(t, nil))

	call(t, tool, map[string]any{"image_source": p, "spec": spec})
	if mock.CallCount() != numBatches(2) {
		t.Fatalf("first call: VLM count=%d want %d", mock.CallCount(), numBatches(2))
	}
	res := call(t, tool, map[string]any{"image_source": p, "spec": spec, "refresh": true})
	if res.IsError {
		t.Fatalf("refresh call failed: %s", resultText(res))
	}
	if want := 2 * numBatches(2); mock.CallCount() != want {
		t.Errorf("refresh must re-invoke VLM for every batch, count=%d want %d", mock.CallCount(), want)
	}
}

func TestCompareDesignInlineSpecJSON(t *testing.T) {
	deps, _ := newCompareDeps(t, compareOKMock("qwen"))
	tool := NewCompareTool(deps)
	p := writeCompareTestImage(t, 375, 812)

	specInline := `{"version":1,"regions":[{"id":"btn","bbox":[37,650,338,772]},{"id":"title","bbox":[37,81,338,162]}]}`
	res := call(t, tool, map[string]any{"image_source": p, "spec": specInline})
	if res.IsError {
		t.Fatalf("inline spec should work: %s", resultText(res))
	}
}

func TestCompareDesignInlineSpecObject(t *testing.T) {
	deps, _ := newCompareDeps(t, compareOKMock("qwen"))
	tool := NewCompareTool(deps)
	p := writeCompareTestImage(t, 375, 812)

	specObj := map[string]any{
		"version": 1,
		"regions": []any{
			map[string]any{"id": "btn", "bbox": []any{37, 650, 338, 772}},
			map[string]any{"id": "title", "bbox": []any{37, 81, 338, 162}},
		},
	}
	res := call(t, tool, map[string]any{"image_source": p, "spec": specObj})
	if res.IsError {
		t.Fatalf("inline spec object should work: %s", resultText(res))
	}
}

func TestCompareDesignInvalidSpecRejected(t *testing.T) {
	deps, _ := newCompareDeps(t, vlm.NewMock("qwen", ""))
	tool := NewCompareTool(deps)
	p := writeCompareTestImage(t, 375, 812)

	res := call(t, tool, map[string]any{"image_source": p, "spec": `{"version":99}`})
	if !res.IsError {
		t.Fatal("invalid spec must return an error")
	}
}

// TestParseRegionReplySmoke asserts the locator parser round-trips a normal
// reply and rejects a malformed one.
func TestParseRegionReplySmoke(t *testing.T) {
	w, h := 375, 812
	dets, err := parseRegionReply(
		`{"regions":[{"id":"a","bbox":[0.1,0.2,0.3,0.4]},{"id":"b","bbox":[0.5,0.6,0.7,0.8]}]}`,
		w, h,
	)
	if err != nil {
		t.Fatalf("parseRegionReply: %v", err)
	}
	if len(dets) != 2 {
		t.Fatalf("got %d dets, want 2", len(dets))
	}
	if dets[0].ID != "a" || dets[1].ID != "b" {
		t.Errorf("ids = %s, %s; want a, b", dets[0].ID, dets[1].ID)
	}
	if _, err := parseRegionReply(`[{"id":"x"}]`, w, h); err == nil {
		t.Error("malformed reply should fail")
	}
}

// TestCompareDesignSingleRegionIDAttribution: with one region per batch, the
// spec's id is authoritative — a model that echoes a different id must not
// turn the region into a silent "missing" verdict.
func TestCompareDesignSingleRegionIDAttribution(t *testing.T) {
	m := vlm.NewMock("qwen", "")
	m.CompleteFn = func(_ context.Context, _ vlm.Request) (*vlm.Response, error) {
		return &vlm.Response{
			Text:  `{"regions":[{"id":"model-invented-this","bbox":[0.1,0.8,0.9,0.95],"text":"hi"}]}`,
			Model: "qwen-mock",
		}, nil
	}
	deps, _ := newCompareDeps(t, m)
	tool := NewCompareTool(deps)
	p := writeCompareTestImage(t, 375, 812)
	spec := writeCompareSpecFile(t, map[string]any{
		"version": 1,
		"regions": []map[string]any{
			{"id": "solo", "desc": "bottom button", "bbox": []float64{37, 650, 338, 772}},
		},
	})

	res := call(t, tool, map[string]any{"image_source": p, "spec": spec})
	if res.IsError {
		t.Fatalf("single-region compare should succeed: %s", resultText(res))
	}
	if txt := resultText(res); !strings.Contains(txt, "1/1 ok") {
		t.Errorf("wrong id echo must still attribute to the spec id: %s", txt)
	}
}

// batchCanned builds an echo table where id "rN" resolves to bbox
// [0.05+N*0.02, 0.1, 0.06+N*0.02, 0.2], so tests can set spec bboxes to the
// exact pixel equivalents and get IoU=1 regardless of how regions are batched.
func batchCanned(n int) map[string]compareCanned {
	canned := make(map[string]compareCanned, n)
	for i := 0; i < n; i++ {
		canned[fmt.Sprintf("r%d", i)] = compareCanned{
			bbox:  [4]float64{0.05 + float64(i)*0.02, 0.1, 0.06 + float64(i)*0.02, 0.2},
			found: true,
		}
	}
	return canned
}

// countCalls wraps a mock's CompleteFn with an invocation counter. The locate
// loop is serial, but the counter stays mutex-guarded so a future concurrent
// batch loop fails loudly here instead of flaking.
func countCalls(t *testing.T, mock *vlm.Mock) func() int {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	orig := mock.CompleteFn
	mock.CompleteFn = func(ctx context.Context, req vlm.Request) (*vlm.Response, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return orig(ctx, req)
	}
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

func TestChunkRegions(t *testing.T) {
	mk := func(n int) []compareRegion {
		out := make([]compareRegion, n)
		for i := range out {
			out[i] = compareRegion{ID: fmt.Sprintf("r%d", i)}
		}
		return out
	}
	cases := []struct{ n, size, want int }{
		{0, 8, 1}, {1, 8, 1}, {8, 8, 1}, {9, 8, 2}, {20, 8, 3}, {29, 8, 4},
	}
	for _, c := range cases {
		got := chunkRegions(mk(c.n), c.size)
		if len(got) != c.want {
			t.Errorf("chunkRegions(n=%d,size=%d) = %d batches, want %d", c.n, c.size, len(got), c.want)
		}
		total := 0
		for _, b := range got {
			total += len(b)
		}
		if total != c.n {
			t.Errorf("n=%d: batches hold %d regions, want %d", c.n, total, c.n)
		}
	}
}

// TestCompareDesignBatchesLargeSpec drives 20 regions through an echo mock,
// proving the spec is split into numBatches(20) calls whose results are merged
// back with every detection attributed to the right id. The mock answers id
// "rN" with bbox [0.05+N*0.02, 0.1, 0.06+N*0.02, 0.2]; spec bboxes are the
// exact pixel equivalents so every IoU is 1.
func TestCompareDesignBatchesLargeSpec(t *testing.T) {
	mockBBox := func(idx int) []float64 {
		x0 := (0.05 + float64(idx)*0.02) * 375
		x1 := (0.06 + float64(idx)*0.02) * 375
		return []float64{x0, 81.2, x1, 162.4}
	}
	regions := make([]map[string]any, 0, 20)
	for i := 0; i < 20; i++ {
		regions = append(regions, map[string]any{
			"id":   fmt.Sprintf("r%d", i),
			"desc": fmt.Sprintf("region %d", i),
			"bbox": mockBBox(i),
		})
	}
	mock := compareEchoMock("qwen", batchCanned(20))
	calls := countCalls(t, mock)

	deps, _ := newCompareDeps(t, mock)
	tool := NewCompareTool(deps)
	p := writeCompareTestImage(t, 375, 812)
	spec := writeCompareSpecFile(t, map[string]any{
		"version": 1,
		"image":   map[string]any{"width": 375, "height": 812},
		"regions": regions,
	})

	res := call(t, tool, map[string]any{"image_source": p, "spec": spec})
	if res.IsError {
		t.Fatalf("compare should succeed: %s", resultText(res))
	}
	if calls() != numBatches(20) {
		t.Errorf("VLM called %d times, want %d (batches of %d)", calls(), numBatches(20), compareBatchSize)
	}
	// Verify merged payload has all 20 regions with OK=20 (perfect IoU)
	txt := resultText(res)
	idx := strings.Index(txt, "Structured result:")
	if idx < 0 {
		t.Fatalf("result missing structured payload: %s", txt)
	}
	var payload comparePayload
	if err := json.Unmarshal([]byte(strings.TrimSpace(txt[idx+len("Structured result:"):])), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Total != 20 {
		t.Errorf("regions_total = %d, want 20", payload.Total)
	}
	if payload.OK != 20 {
		t.Errorf("regions_ok = %d, want 20 (got issues: %v)", payload.OK, payload.Regions)
	}
}

// TestCompareDesignBatchCacheIsPerBatch ensures a repeat run is served from L2
// per batch, so no extra VLM calls happen.
func TestCompareDesignBatchCacheIsPerBatch(t *testing.T) {
	mock := compareEchoMock("qwen", batchCanned(12))
	deps, _ := newCompareDeps(t, mock)
	tool := NewCompareTool(deps)
	p := writeCompareTestImage(t, 375, 812)

	regions := make([]map[string]any, 0, 12)
	for i := 0; i < 12; i++ {
		regions = append(regions, map[string]any{
			"id":   fmt.Sprintf("r%d", i),
			"bbox": []float64{10 + float64(i), 20, 40 + float64(i), 60},
		})
	}
	spec := writeCompareSpecFile(t, map[string]any{
		"version": 1, "image": map[string]any{"width": 375, "height": 812}, "regions": regions,
	})
	args := map[string]any{"image_source": p, "spec": spec}

	if res := call(t, tool, args); res.IsError {
		t.Fatalf("first call failed: %s", resultText(res))
	}
	afterFirst := mock.CallCount()
	res2 := call(t, tool, args)
	if res2.IsError {
		t.Fatalf("second call failed: %s", resultText(res2))
	}
	if mock.CallCount() != afterFirst {
		t.Errorf("repeat call must be fully cached, VLM count %d -> %d", afterFirst, mock.CallCount())
	}
}
