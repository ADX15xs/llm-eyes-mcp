package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/xiaos/llm-eyes-mcp/internal/cache"
	"github.com/xiaos/llm-eyes-mcp/internal/geometry"
	"github.com/xiaos/llm-eyes-mcp/internal/imageio"
	"github.com/xiaos/llm-eyes-mcp/internal/imgproc"
	"github.com/xiaos/llm-eyes-mcp/internal/mcp"
	"github.com/xiaos/llm-eyes-mcp/internal/vlm"
)

// comparePayload is the cacheable, renderable result of one comparison.
type comparePayload struct {
	Summary     string       `json:"summary"`
	Score       float64      `json:"score"` // ok / total, Go-computed
	Total       int          `json:"regions_total"`
	OK          int          `json:"regions_ok"`
	ImageID     string       `json:"image_id"`
	ImageWidth  int          `json:"image_width"`
	ImageHeight int          `json:"image_height"`
	SpecHash    string       `json:"spec_hash"`
	Regions     []regionDiff `json:"regions"`
	Model       string       `json:"model,omitempty"`
	CacheTier   string       `json:"cache_tier"`
	Rounds      int          `json:"vlm_rounds,omitempty"`
}

// CompareTool diffs a structured design spec (machine-readable design truth)
// against a screenshot of the implemented page. The VLM only locates regions
// and transcribes their text; Go computes every comparison number.
//
// Design contract:
//   - Spec is a JSON file produced by an Agent-side converter (e.g. the sloth
//     HTML → spec reference script), not by llm-eyes. We do not know any
//     D2C tool's private format.
//   - The screenshot must be component-level granularity (see docs/spec-schema.md).
//   - All measurement numbers come from Go, never from the model.
type CompareTool struct{ d *Deps }

// NewCompareTool builds the compare tool.
func NewCompareTool(d *Deps) *CompareTool { return &CompareTool{d: d} }

func (t *CompareTool) Name() string { return "compare_design" }

func (t *CompareTool) Description() string {
	return "Compare a structured design spec against an actual screenshot and report per-region restore fidelity. " +
		"The spec is a JSON file or inline object produced by a D2C exporter converter (reference script at examples/sloth_to_spec.py). " +
		"The VLM only locates regions and transcribes text inside them; Go computes colour delta, IoU, and text equality. " +
		"Use this AFTER implementing a design component from a D2C export, on a CROPPED screenshot of ONE component " +
		"together with the spec slice for that component. " +
		"Do NOT use it for open-ended image questions (use describe_image), geometry between arbitrary objects (use measure_image), or full-page comparisons. " +
		"See docs/spec-schema.md for the spec contract."
}

func (t *CompareTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"image_source": map[string]any{
				"type":        "string",
				"description": "The screenshot to compare. Accepts an http(s) URL, an absolute file path, a file:// URI, a data URI, raw base64, or a previously returned image_id.",
			},
			"spec": map[string]any{
				"type":        "string",
				"description": "Path to the spec JSON file, or the spec JSON object itself inline. See docs/spec-schema.md for the format.",
			},
			"refresh": map[string]any{
				"type":        "boolean",
				"description": "Optional: set true to bypass the cache and re-run the vision model.",
			},
			"provider": map[string]any{
				"type":        "string",
				"description": "Optional: name of a specific configured vision provider. Defaults to the server's default provider.",
			},
		},
		"required": []string{"image_source", "spec"},
	}
}

// compareSpec mirrors docs/spec-schema.md. Kept small and flat to avoid
// coupling to any single D2C tool.
type compareSpec struct {
	Version int            `json:"version"`
	Image   *specImage     `json:"image,omitempty"`
	Regions []compareRegion `json:"regions"`
}

type specImage struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type compareRegion struct {
	ID    string   `json:"id"`
	Desc  string   `json:"desc,omitempty"`
	BBox  []float64 `json:"bbox,omitempty"`
	Color string   `json:"color,omitempty"`
	Text  string   `json:"text,omitempty"`
}

func (r compareRegion) hasExpectation() bool {
	return len(r.BBox) == 4 || r.Color != "" || r.Text != ""
}

// Call runs the hard pipeline: spec parse -> VLM locate -> Go compare.
func (t *CompareTool) Call(ctx context.Context, args map[string]any) *mcp.ToolResult {
	source := imageSourceArg(args)
	if source == "" {
		return mcp.ErrorResult("image_source is required: pass a URL, file path, data URI, base64 string, or a previously returned image_id.")
	}
	specSrc := strings.TrimSpace(getString(args, "spec"))
	if specSrc == "" {
		return mcp.ErrorResult("spec is required: pass a JSON file path or an inline JSON object.")
	}

	// Read / parse spec.
	spec, specHash, err := loadSpec(specSrc)
	if err != nil {
		return mcp.ErrorResult("failed to parse spec: " + err.Error())
	}
	if err := validateSpec(spec); err != nil {
		return mcp.ErrorResult("invalid spec: " + err.Error())
	}

	img, err := t.d.resolveImage(source)
	if err != nil {
		return mcp.ErrorResult("Failed to load image: " + err.Error())
	}
	provider, err := t.d.providerFor(args)
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}
	refresh := getBool(args, "refresh")

	// L2 key covers image + spec content + provider.
	l2Key := cache.L2Key(img.ID, "compare_design", provider.ModelVersion(),
		cache.ParamHash(map[string]string{"spec_hash": specHash}))

	if d := t.d.Cache; d != nil && !refresh {
		if blob, ok := d.L2.Get(l2Key); ok {
			var cached comparePayload
			if json.Unmarshal(blob, &cached) == nil {
				cached.CacheTier = "L2"
				t.d.logf("compare L2 hit for %s", img.ID[:8])
				return renderCompare(&cached)
			}
		}
	}

	// Hard pipeline: preserve edges for VLM locator + OCR clarity.
	proc, err := t.d.preprocess(img, imgproc.HardOptions())
	if err != nil {
		return mcp.ErrorResult("Failed to preprocess image: " + err.Error())
	}

	dets, meta, err := t.d.locateRegions(ctx, img.ID, proc, provider, spec, specHash, refresh)
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}

	diff := t.diffRegions(spec, img, dets)
	payload := &comparePayload{
		Summary:     summarize(diff),
		Score:       float64(diff.ok) / float64(len(diff.regions)),
		Total:       len(diff.regions),
		OK:          diff.ok,
		ImageID:     img.ID,
		ImageWidth:  img.Width,
		ImageHeight: img.Height,
		SpecHash:    specHash,
		Regions:     diff.regions,
		Model:       meta.model,
		CacheTier:   meta.tier,
		Rounds:      meta.rounds,
	}

	if d := t.d.Cache; d != nil {
		if blob, err := json.Marshal(payload); err == nil {
			d.L2.Put(l2Key, blob)
		}
	}
	return renderCompare(payload)
}

// ---------------------------------------------------------------------------
// Spec I/O
// ---------------------------------------------------------------------------

func loadSpec(src string) (*compareSpec, string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		// Fallback: treat src as inline JSON.
		data = []byte(src)
	}
	var spec compareSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, "", fmt.Errorf("spec parse failed: %w", err)
	}
	h := sha256.Sum256(data)
	return &spec, hex.EncodeToString(h[:]), nil
}

func validateSpec(s *compareSpec) error {
	if s.Version != 1 {
		return fmt.Errorf("unsupported version %d (expected 1)", s.Version)
	}
	if len(s.Regions) == 0 {
		return fmt.Errorf("spec requires at least 1 region")
	}
	if len(s.Regions) > 50 {
		return fmt.Errorf("spec has %d regions (max 50); split into smaller components", len(s.Regions))
	}
	seen := map[string]bool{}
	for i, r := range s.Regions {
		if r.ID == "" {
			return fmt.Errorf("region[%d] missing id", i)
		}
		if seen[r.ID] {
			return fmt.Errorf("duplicate region id %q", r.ID)
		}
		seen[r.ID] = true
		if len(r.BBox) > 0 && len(r.BBox) != 4 {
			return fmt.Errorf("region %q bbox has %d coords (expect 4)", r.ID, len(r.BBox))
		}
		if r.Color != "" && !hexColorRe.MatchString(r.Color) {
			return fmt.Errorf("region %q bad color %q", r.ID, r.Color)
		}
		if !r.hasExpectation() {
			return fmt.Errorf("region %q has none of bbox/color/text; meaningless to compare", r.ID)
		}
	}
	return nil
}

var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// ---------------------------------------------------------------------------
// VLM locate (parallel structure to detectObjects in measure.go)
// ---------------------------------------------------------------------------

type regionDetection struct {
	ID       string
	Box      geometry.Box // normalised [0,1]
	Text     string
	Confidence float64
	Found      bool
}

type compareDetectMeta struct {
	tier   string
	model  string
	rounds int
}

// compareBatchSize bounds how many regions ride in one VLM call. The uoma
// gateway ignores every thinking-disable parameter on vision requests, so a
// multi-region locate prompt burns its whole token budget on reasoning and
// returns NULL content (probes archived under docs/archive; see docs/TODO.md).
// One region per call is the only size verified reliable against that upstream
// — revisit when the gateway honours thinking controls.
const compareBatchSize = 1

// chunkRegions splits rs into consecutive slices of at most size elements.
func chunkRegions(rs []compareRegion, size int) [][]compareRegion {
	if size <= 0 || len(rs) == 0 {
		return [][]compareRegion{rs}
	}
	out := make([][]compareRegion, 0, (len(rs)+size-1)/size)
	for start := 0; start < len(rs); start += size {
		end := start + size
		if end > len(rs) {
			end = len(rs)
		}
		out = append(out, rs[start:end])
	}
	return out
}

func (d *Deps) locateRegions(
	ctx context.Context,
	imgID string,
	proc *imgproc.Output,
	provider vlm.Provider,
	spec *compareSpec,
	specHash string,
	refresh bool,
) ([]regionDetection, compareDetectMeta, error) {
	batches := chunkRegions(spec.Regions, compareBatchSize)
	agg := compareDetectMeta{tier: "L2", model: provider.ModelVersion()}
	out := make([]regionDetection, 0, len(spec.Regions))

	for i, batch := range batches {
		batchSpec := &compareSpec{Version: spec.Version, Regions: batch}
		dets, meta, err := d.locateRegionBatch(ctx, imgID, proc, provider, batchSpec, specHash, i, refresh)
		if err != nil {
			return nil, agg, err
		}
		if meta.model != "" {
			agg.model = meta.model
		}
		agg.rounds += meta.rounds
		out = append(out, dets...)
	}
	if len(batches) > 1 {
		d.logf("compare located %d regions across %d batches (<=%d each)", len(out), len(batches), compareBatchSize)
	}
	return out, agg, nil
}

// forceSingleRegionID makes the spec's id authoritative when a batch holds
// exactly one region: whatever box the model returned is by construction the
// answer to "locate this region", so a wrong or missing id echo must not turn
// into a silent "missing" verdict downstream.
func forceSingleRegionID(spec *compareSpec, dets []regionDetection) {
	if len(spec.Regions) == 1 && len(dets) == 1 {
		dets[0].ID = spec.Regions[0].ID
		dets[0].Box.Label = spec.Regions[0].ID
	}
}

// locateRegionBatch locates one slice of regions, with its own L2 entry and
// bounded re-asks, so a hard batch cannot poison its neighbours.
func (d *Deps) locateRegionBatch(
	ctx context.Context,
	imgID string,
	proc *imgproc.Output,
	provider vlm.Provider,
	spec *compareSpec,
	specHash string,
	batch int,
	refresh bool,
) ([]regionDetection, compareDetectMeta, error) {
	meta := compareDetectMeta{tier: "L2", model: provider.ModelVersion()}

	l2Key := cache.L2Key(imgID, "compare_design_locate", provider.ModelVersion(),
		cache.ParamHash(map[string]string{
			"spec_hash": specHash,
			"batch":     strconv.Itoa(batch),
			"batch_n":   strconv.Itoa(len(spec.Regions)),
		}))
	if d.Cache != nil && !refresh {
		if blob, ok := d.Cache.L2.Get(l2Key); ok {
			var entry measureCacheEntry
			if json.Unmarshal(blob, &entry) == nil && entry.Text != "" && entry.W > 0 && entry.H > 0 {
				if dets, err := parseRegionReply(entry.Text, entry.W, entry.H); err == nil && len(dets) == len(spec.Regions) {
					forceSingleRegionID(spec, dets)
					return dets, meta, nil
				}
			}
			d.Cache.L2.Delete(l2Key)
		}
	}

	maxRounds := d.maxRounds()
	var lastReply string
	var lastErr error

	for round := 1; round <= maxRounds; round++ {
		meta.rounds = round
		prompt := compareLocatePrompt(spec)
		if round > 1 {
			prompt = compareLocateRetryPrompt(spec, lastMissingIDs(spec, lastReply, proc.Width, proc.Height), lastReply)
		}
		resp, err := provider.Complete(ctx, vlm.Request{
			System:    compareSystemPrompt,
			Prompt:    prompt,
			Image:     proc.Data,
			ImageMIME: proc.MIMEType,
			JSONOnly:  true,
		})
		if err != nil {
			lastErr = err
			d.logf("compare batch %d round %d/%d failed: %v", batch, round, maxRounds, err)
			continue
		}
		meta.model = resp.Model
		lastReply = resp.Text

		dets, err := parseRegionReply(resp.Text, proc.Width, proc.Height)
		if err != nil {
			lastErr = err
			d.logf("compare batch %d round %d/%d unparsable: %v", batch, round, maxRounds, err)
			continue
		}
		if len(dets) == len(spec.Regions) {
			forceSingleRegionID(spec, dets)
			if d.Cache != nil {
				if blob, err := json.Marshal(measureCacheEntry{Text: resp.Text, W: proc.Width, H: proc.Height}); err == nil {
					d.Cache.L2.Put(l2Key, blob)
				}
			}
			return dets, meta, nil
		}
		lastErr = fmt.Errorf("model located %d/%d regions", len(dets), len(spec.Regions))
		d.logf("compare batch %d round %d/%d partial: %d/%d", batch, round, maxRounds, len(dets), len(spec.Regions))
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no usable reply returned")
	}
	return nil, meta, fmt.Errorf(
		"Region location failed after %d round(s): %v. Check that the screenshot actually contains the regions named in the spec.",
		maxRounds, lastErr)
}

// ---------------------------------------------------------------------------
// Parsing VLM reply (mirrors parseDetections but for region JSON)
// ---------------------------------------------------------------------------

type rawRegion struct {
	ID         string          `json:"id"`
	Label      string          `json:"label"`
	Name       string          `json:"name"`
	BBox       json.RawMessage `json:"bbox"`
	Box        json.RawMessage `json:"box"`
	BoundingBox json.RawMessage `json:"bounding_box"`
	Bbox2D     json.RawMessage `json:"bbox_2d"`
	Text       string          `json:"text"`
	Confidence *float64        `json:"confidence"`
	Score      *float64        `json:"score"`
}
type rawRegionEnvelope struct {
	Regions []rawRegion `json:"regions"`
	Objects []rawRegion `json:"objects"`
	Results []rawRegion `json:"results"`
	Items   []rawRegion `json:"items"`
}

func parseRegionReply(reply string, imgW, imgH int) ([]regionDetection, error) {
	doc := extractJSON(reply)
	if doc == "" {
		return nil, fmt.Errorf("model returned no JSON")
	}
	var raws []rawRegion
	if strings.HasPrefix(doc, "[") {
		if err := json.Unmarshal([]byte(doc), &raws); err != nil {
			return nil, fmt.Errorf("parse JSON array: %w", err)
		}
	} else {
		var env rawRegionEnvelope
		if err := json.Unmarshal([]byte(doc), &env); err != nil {
			return nil, fmt.Errorf("parse JSON object: %w", err)
		}
		switch {
		case len(env.Regions) > 0:
			raws = env.Regions
		case len(env.Objects) > 0:
			raws = env.Objects
		case len(env.Results) > 0:
			raws = env.Results
		case len(env.Items) > 0:
			raws = env.Items
		default:
			var one rawRegion
			if err := json.Unmarshal([]byte(doc), &one); err == nil && one.anyBox() != nil {
				raws = []rawRegion{one}
			}
		}
	}
	if len(raws) == 0 {
		return nil, fmt.Errorf("model returned no regions")
	}

	out := make([]regionDetection, 0, len(raws))
	for _, r := range raws {
		id := firstNonEmpty(r.ID, r.Label, r.Name)
		if id == "" {
			continue
		}
		coords, err := decodeBox(r.anyBox())
		if err != nil {
			continue
		}
		conf := 0.0
		if r.Confidence != nil {
			conf = *r.Confidence
		} else if r.Score != nil {
			conf = *r.Score
		}
		nx, err := normaliseCoords(coords, imgW, imgH)
		if err != nil {
			continue
		}
		degenerate := nx[0] == 0 && nx[1] == 0 && nx[2] == 0 && nx[3] == 0
		out = append(out, regionDetection{
			ID:         strings.TrimSpace(id),
			Box:        geometry.Box{Label: strings.TrimSpace(id), X0: nx[0], Y0: nx[1], X1: nx[2], Y1: nx[3]},
			Text:       r.Text,
			Confidence: conf,
			Found:      !degenerate && conf >= 0,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("model returned regions but none had a usable bbox")
	}
	return out, nil
}

func (r rawRegion) anyBox() json.RawMessage {
	for _, b := range []json.RawMessage{r.BBox, r.Box, r.BoundingBox, r.Bbox2D} {
		if len(b) > 0 && string(b) != "null" {
			return b
		}
	}
	return nil
}

func lastMissingIDs(spec *compareSpec, reply string, w, h int) []string {
	if reply == "" {
		return specIDs(spec)
	}
	dets, err := parseRegionReply(reply, w, h)
	if err != nil {
		return specIDs(spec)
	}
	found := map[string]bool{}
	for _, d := range dets {
		found[d.ID] = true
	}
	var missing []string
	for _, r := range spec.Regions {
		if !found[r.ID] {
			missing = append(missing, r.ID)
		}
	}
	return missing
}

func specIDs(spec *compareSpec) []string {
	out := make([]string, len(spec.Regions))
	for i, r := range spec.Regions {
		out[i] = r.ID
	}
	return out
}

// ---------------------------------------------------------------------------
// Go-side comparison
// ---------------------------------------------------------------------------

type regionDiff struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	Found      bool      `json:"found"`
	Confidence float64   `json:"confidence,omitempty"`
	BBox       []float64 `json:"bbox,omitempty"` // located, original px
	SpecBBox   []float64 `json:"spec_bbox,omitempty"`
	IOU        float64   `json:"iou,omitempty"`
	CenterDist float64   `json:"center_dist_px,omitempty"`
	Issues     []string  `json:"issues,omitempty"`
	// Color
	ExpectedColor string  `json:"expected_color,omitempty"`
	ActualColor   string  `json:"actual_color,omitempty"`
	ColorDelta    float64 `json:"color_delta,omitempty"`
	// Text
	ExpectedText string `json:"expected_text,omitempty"`
	ActualText   string `json:"actual_text,omitempty"`
}

type compareDiff struct {
	regions []regionDiff
	ok      int
}

func (t *CompareTool) diffRegions(spec *compareSpec, img *imageio.Image, dets []regionDetection) compareDiff {
	var diff compareDiff
	src, _, decodeErr := image.Decode(bytes.NewReader(img.Data))
	if decodeErr != nil {
		t.d.logf("warning: could not decode original image for color sampling: %v", decodeErr)
	}

	scaleX := 1.0
	scaleY := 1.0
	if spec.Image != nil && spec.Image.Width > 0 && spec.Image.Height > 0 {
		scaleX = float64(img.Width) / float64(spec.Image.Width)
		scaleY = float64(img.Height) / float64(spec.Image.Height)
	}

	foundMap := map[string]regionDetection{}
	for _, d := range dets {
		foundMap[d.ID] = d
	}

	for _, r := range spec.Regions {
		diff.regions = append(diff.regions, t.diffOne(r, foundMap, img, src, scaleX, scaleY))
	}
	diff.ok = 0
	for _, rd := range diff.regions {
		if rd.Status == "ok" {
			diff.ok++
		}
	}
	return diff
}

func (t *CompareTool) diffOne(r compareRegion, found map[string]regionDetection, img *imageio.Image, src image.Image, sx, sy float64) regionDiff {
	rd := regionDiff{ID: r.ID}
	det, has := found[r.ID]
	rd.Found = has && det.Found
	rd.Confidence = det.Confidence
	if has && det.Found {
		box := det.Box.Scale(img.Width, img.Height)
		rd.BBox = []float64{box.X0, box.Y0, box.X1, box.Y1}
		rd.ActualText = det.Text
	}

	if r.BBox != nil && len(r.BBox) == 4 {
		sb := specBoxToPixel(r.BBox, sx, sy)
		rd.SpecBBox = []float64{sb[0], sb[1], sb[2], sb[3]}
		if has && det.Found {
			ib := boxToPixel(det.Box, img.Width, img.Height)
			iou := intersectionOverUnion(sb, ib)
			rd.IOU = iou
			cd := centerDistPxDist(sb, ib)
			rd.CenterDist = cd
			if iou < 0.5 {
				rd.Status = "offset"
				rd.Issues = append(rd.Issues, fmt.Sprintf("offset(iou=%.2f,center=%.1fpx)", iou, cd))
			}
		}
	}

	if r.Color != "" {
		rd.ExpectedColor = r.Color
		if has && det.Found {
			ib := boxToPixel(det.Box, img.Width, img.Height)
			if src != nil {
				rect := image.Rectangle{
					Min: image.Point{X: int(ib[0]), Y: int(ib[1])},
					Max: image.Point{X: int(ib[2]), Y: int(ib[3])},
				}
				if !rect.Empty() && !rect.Intersect(src.Bounds()).Empty() {
					sample := imgproc.AverageRGB(src, rect, 0.2)
					rd.ActualColor = sample.Hex()
					rd.ColorDelta = sample.Delta(hexToRGB(r.Color))
				}
			}
			if rd.ColorDelta > 24 {
				if rd.Status == "" {
					rd.Status = "color_mismatch"
				} else {
					rd.Status += "+color_mismatch"
				}
				rd.Issues = append(rd.Issues, fmt.Sprintf("color_delta=%.1f(expected=%s actual=%s)", rd.ColorDelta, r.Color, rd.ActualColor))
			}
		}
	}

	if r.Text != "" {
		rd.ExpectedText = normalizeWhitespace(r.Text)
		if has && det.Found {
			got := normalizeWhitespace(det.Text)
			rd.ActualText = got
			if got != rd.ExpectedText {
				if rd.Status == "" {
					rd.Status = "text_mismatch"
				} else {
					rd.Status += "+text_mismatch"
				}
				rd.Issues = append(rd.Issues, fmt.Sprintf("text(expected=%q got=%q)", r.Text, det.Text))
			}
		}
	}

	if rd.Status == "" {
		if rd.Found {
			rd.Status = "ok"
		} else {
			rd.Status = "missing"
			rd.Issues = append(rd.Issues, "not found in screenshot")
		}
	}
	return rd
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func specBoxToPixel(bbox []float64, sx, sy float64) [4]float64 {
	return [4]float64{
		bbox[0] * sx,
		bbox[1] * sy,
		bbox[2] * sx,
		bbox[3] * sy,
	}
}

func boxToPixel(b geometry.Box, pw, ph int) [4]float64 {
	return [4]float64{
		b.X0 * float64(pw),
		b.Y0 * float64(ph),
		b.X1 * float64(pw),
		b.Y1 * float64(ph),
	}
}

func intersectionOverUnion(a, b [4]float64) float64 {
	ix0 := math.Max(a[0], b[0])
	iy0 := math.Max(a[1], b[1])
	ix1 := math.Min(a[2], b[2])
	iy1 := math.Min(a[3], b[3])
	inter := math.Max(0, ix1-ix0) * math.Max(0, iy1-iy0)
	areaA := math.Max(0, a[2]-a[0]) * math.Max(0, a[3]-a[1])
	areaB := math.Max(0, b[2]-b[0]) * math.Max(0, b[3]-b[1])
	union := areaA + areaB - inter
	if union == 0 {
		return 0
	}
	return inter / union
}

func centerDistPxDist(a, b [4]float64) float64 {
	ca := [2]float64{(a[0] + a[2]) / 2, (a[1] + a[3]) / 2}
	cb := [2]float64{(b[0] + b[2]) / 2, (b[1] + b[3]) / 2}
	dx := ca[0] - cb[0]
	dy := ca[1] - cb[1]
	return math.Sqrt(dx*dx + dy*dy)
}

func normalizeWhitespace(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	var lastSpace bool
	for _, r := range s {
		switch {
		case r == '\t', r == '\n', r == '\r', r == ' ', r == '\u00A0', r == '\u3000':
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			b.WriteRune(r)
			lastSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

func hexToRGB(h string) imgproc.RGB {
	var r, g, b int
	fmt.Sscanf(h, "#%02x%02x%02x", &r, &g, &b)
	return imgproc.RGB{R: float64(r), G: float64(g), B: float64(b)}
}

func summarize(d compareDiff) string {
	total := len(d.regions)
	parts := make([]string, 0, len(d.regions))
	for _, r := range d.regions {
		if r.Status == "ok" {
			continue
		}
		if len(r.Issues) == 0 {
			parts = append(parts, fmt.Sprintf("%s: %s", r.ID, r.Status))
		} else {
			parts = append(parts, fmt.Sprintf("%s(%s)", r.ID, strings.Join(r.Issues, ",")))
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("All %d regions match", total)
	}
	pct := 0.0
	if total > 0 {
		pct = float64(d.ok) / float64(total) * 100
	}
	return fmt.Sprintf("%d/%d regions ok (%.0f%%). Issues: %s", d.ok, total, pct, strings.Join(parts, "; "))
}

func renderCompare(p *comparePayload) *mcp.ToolResult {
	notes := []string{
		fmt.Sprintf("image_id=%s (%dx%d px)", p.ImageID, p.ImageWidth, p.ImageHeight),
		"spec_hash=" + p.SpecHash[:min(8, len(p.SpecHash))],
		"cache=" + p.CacheTier,
	}
	if p.Model != "" {
		notes = append(notes, "model="+p.Model)
	}
	if p.Rounds > 0 {
		notes = append(notes, "vlm_rounds="+fmt.Sprintf("%d", p.Rounds))
	}
	headline := fmt.Sprintf("compare_design score=%.2f (%d/%d ok)\n%s", p.Score, p.OK, p.Total, p.Summary)
	headline += "\n[" + strings.Join(notes, "; ") + "]"

	blob, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return mcp.TextResult(headline)
	}
	return &mcp.ToolResult{Content: []map[string]any{
		mcp.TextContent(headline),
		mcp.TextContent("Structured result:\n" + string(blob)),
	}}
}


