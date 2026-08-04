package tools

import (
	"strings"
	"testing"

	"github.com/xiaos/llm-eyes-mcp/internal/vlm"
)

// measure_test drives the hard pipeline end-to-end: a mock VLM returns
// coordinates, Go computes the distance, and repeats are served from cache.

func TestMeasureImageComputesDistance(t *testing.T) {
	deps := newDeps(t, measureMock("glm"))
	tool := NewMeasureTool(deps)
	p := writeTestPNG(t, 320, 320)

	res := call(t, tool, map[string]any{
		"image_source":  p,
		"measure_type":  "distance",
		"target_labels": []any{"screw_A", "screw_B"},
	})
	if res == nil {
		t.Fatal("measure returned nil result")
	}
	if res.IsError {
		t.Fatalf("measure should succeed, got error: %s", resultText(res))
	}
	txt := resultText(res)
	if !strings.Contains(txt, "distance") || !strings.Contains(txt, "px") {
		t.Errorf("result should report a pixel distance: %s", txt)
	}
	if !strings.Contains(txt, "cache=vlm") {
		t.Errorf("first call must hit the VLM tier: %s", txt)
	}
}

func TestMeasureImageCachesAcrossCalls(t *testing.T) {
	mock := measureMock("glm")
	deps := newDeps(t, mock)
	tool := NewMeasureTool(deps)
	p := writeTestPNG(t, 320, 320)
	args := map[string]any{
		"image_source":  p,
		"measure_type":  "distance",
		"target_labels": []any{"screw_A", "screw_B"},
	}
	// First call reaches the VLM (L2 + L3 populated).
	if res := call(t, tool, args); res.IsError {
		t.Fatalf("first call failed: %s", resultText(res))
	}
	if mock.CallCount() != 1 {
		t.Fatalf("first call must invoke the VLM exactly once: count=%d", mock.CallCount())
	}
	// Second call with the identical image+labels is served from the geometry
	// cache (L3) without touching the VLM again.
	res2 := call(t, tool, args)
	if res2.IsError {
		t.Fatalf("second call failed: %s", resultText(res2))
	}
	if mock.CallCount() != 1 {
		t.Errorf("repeat call must be cached (L3), VLM count=%d want 1", mock.CallCount())
	}
	if !strings.Contains(resultText(res2), "cache=L3") {
		t.Errorf("repeat call must report L3 cache tier: %s", resultText(res2))
	}
}

func TestMeasureImageRefreshBypassesCache(t *testing.T) {
	mock := measureMock("glm")
	deps := newDeps(t, mock)
	tool := NewMeasureTool(deps)
	p := writeTestPNG(t, 320, 320)
	base := map[string]any{
		"image_source":  p,
		"measure_type":  "distance",
		"target_labels": []any{"screw_A", "screw_B"},
	}
	if res := call(t, tool, base); res.IsError {
		t.Fatalf("first call failed: %s", resultText(res))
	}
	// refresh=true forces a re-query even though the cache is warm.
	refresh := map[string]any{
		"image_source":  p,
		"measure_type":  "distance",
		"target_labels": []any{"screw_A", "screw_B"},
		"refresh":       true,
	}
	if res := call(t, tool, refresh); res.IsError {
		t.Fatalf("refresh call failed: %s", resultText(res))
	}
	if mock.CallCount() != 2 {
		t.Errorf("refresh must hit the VLM again: count=%d want 2", mock.CallCount())
	}
}

func TestMeasureImageRejectsBadInput(t *testing.T) {
	deps := newDeps(t, measureMock("glm"))
	tool := NewMeasureTool(deps)

	// Missing image_source.
	if res := call(t, tool, map[string]any{"measure_type": "distance", "target_labels": []any{"a", "b"}}); !res.IsError {
		t.Errorf("missing image_source must be a business error")
	}
	// Missing measure_type.
	if res := call(t, tool, map[string]any{"image_source": writeTestPNG(t, 10, 10), "target_labels": []any{"a", "b"}}); !res.IsError {
		t.Errorf("missing measure_type must be a business error")
	}
	// distance requires exactly 2 labels.
	if res := call(t, tool, map[string]any{
		"image_source":  writeTestPNG(t, 10, 10),
		"measure_type":  "distance",
		"target_labels": []any{"a"},
	}); !res.IsError {
		t.Errorf("distance with 1 label must be a business error")
	}
}

func TestMeasureImageVLMFailureIsBusinessError(t *testing.T) {
	// The VLM always errors; the tool must surface it as a retryable business
	// failure (IsError) rather than crash the server.
	mock := vlm.NewFailingMock("glm", "vision backend down")
	deps := newDeps(t, mock)
	tool := NewMeasureTool(deps)
	res := call(t, tool, map[string]any{
		"image_source":  writeTestPNG(t, 320, 320),
		"measure_type":  "distance",
		"target_labels": []any{"screw_A", "screw_B"},
	})
	if res == nil || !res.IsError {
		t.Errorf("VLM failure must become a business error result")
	}
}
