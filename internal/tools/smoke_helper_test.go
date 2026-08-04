package tools

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaos/llm-eyes-mcp/internal/cache"
	"github.com/xiaos/llm-eyes-mcp/internal/imageio"
	"github.com/xiaos/llm-eyes-mcp/internal/mcp"
	"github.com/xiaos/llm-eyes-mcp/internal/vlm"
)

// smoke_helper_test wires the three tools to an in-memory cache and a mock VLM
// so every tool can be exercised end-to-end without the network.

// writeTestPNG writes a deterministic small PNG and returns its path. The bytes
// are stable, so loading the same file twice yields the same cache key.
func writeTestPNG(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// A diagonal gradient so the image is non-trivial (real entropy) and the
	// preprocessing pipeline has something to chew on.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(255 * x / w),
				G: uint8(255 * y / h),
				B: uint8(128),
				A: 255,
			})
		}
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "sample.png")
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

// newDeps builds a full Deps backed by a temp cache and the given mock VLM.
func newDeps(t *testing.T, mock *vlm.Mock) *Deps {
	t.Helper()
	mgr, err := cache.Open(cache.Settings{Root: t.TempDir(), L3MaxEntries: 64})
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	loader := imageio.NewLoader(30 * time.Second)
	loader.Archive = mgr.ArchiveLookup()
	set := vlm.NewSet(mock.Name(), mock)
	return &Deps{
		Loader:    loader,
		Cache:     mgr,
		Providers: set,
		MaxRounds: 2,
		Logf:      t.Logf,
	}
}

// resultText flattens a ToolResult's text content for assertions.
func resultText(tr *mcp.ToolResult) string {
	if tr == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range tr.Content {
		if s, ok := c["text"].(string); ok {
			b.WriteString(s)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// call is a convenience wrapper that ignores the context plumbing.
func call(t *testing.T, tool mcp.Tool, args map[string]any) *mcp.ToolResult {
	t.Helper()
	return tool.Call(context.Background(), args)
}

// measureMock returns a mock whose JSONOnly replies carry two located boxes so
// the hard pipeline can compute a real distance.
func measureMock(name string) *vlm.Mock {
	m := vlm.NewMock(name, "")
	m.CompleteFn = func(_ context.Context, req vlm.Request) (*vlm.Response, error) {
		if req.JSONOnly {
			return &vlm.Response{
				Text:  `{"detections":[{"label":"screw_A","bbox":[10,10,60,60]},{"label":"screw_B","bbox":[200,200,260,260]}]}`,
				Model: name + "-mock",
			}, nil
		}
		return &vlm.Response{Text: "raw vision prose", Model: name + "-mock"}, nil
	}
	return m
}
