package tools

import (
	"strings"
	"testing"

	"github.com/xiaos/llm-eyes-mcp/internal/vlm"
)

// describe_test drives describe_image end-to-end and proves the L2 VLM cache
// turns a repeat call into a no-op at the backend.

func TestDescribeImageReturnsText(t *testing.T) {
	mock := vlm.NewMock("glm", "A red square sits on a white background near the top-left corner.")
	deps := newDeps(t, mock)
	tool := NewDescribeTool(deps)
	p := writeTestPNG(t, 200, 200)

	res := call(t, tool, map[string]any{"image_source": p})
	if res == nil {
		t.Fatal("describe returned nil")
	}
	if res.IsError {
		t.Fatalf("describe should succeed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "red square") {
		t.Errorf("result should carry the VLM description: %s", resultText(res))
	}
	if mock.CallCount() != 1 {
		t.Errorf("describe must call the VLM once: count=%d", mock.CallCount())
	}
}

func TestDescribeImageCachesAcrossCalls(t *testing.T) {
	mock := vlm.NewMock("glm", "A blue circle in the centre.")
	deps := newDeps(t, mock)
	tool := NewDescribeTool(deps)
	p := writeTestPNG(t, 200, 200)

	if res := call(t, tool, map[string]any{"image_source": p}); res.IsError {
		t.Fatalf("first call failed: %s", resultText(res))
	}
	if mock.CallCount() != 1 {
		t.Fatalf("first call must hit VLM once: count=%d", mock.CallCount())
	}
	// Identical image -> L2 hit, VLM not re-invoked.
	res2 := call(t, tool, map[string]any{"image_source": p})
	if res2.IsError {
		t.Fatalf("second call failed: %s", resultText(res2))
	}
	if mock.CallCount() != 1 {
		t.Errorf("repeat describe must be served from L2: VLM count=%d want 1", mock.CallCount())
	}
}

func TestDescribeImageRefreshBypassesCache(t *testing.T) {
	mock := vlm.NewMock("glm", "A green triangle.")
	deps := newDeps(t, mock)
	tool := NewDescribeTool(deps)
	p := writeTestPNG(t, 200, 200)

	if res := call(t, tool, map[string]any{"image_source": p}); res.IsError {
		t.Fatalf("first call failed: %s", resultText(res))
	}
	if res := call(t, tool, map[string]any{"image_source": p, "refresh": true}); res.IsError {
		t.Fatalf("refresh call failed: %s", resultText(res))
	}
	if mock.CallCount() != 2 {
		t.Errorf("refresh must re-query the VLM: count=%d want 2", mock.CallCount())
	}
}

func TestDescribeImageMissingSourceIsBusinessError(t *testing.T) {
	deps := newDeps(t, vlm.NewMock("glm", "x"))
	tool := NewDescribeTool(deps)
	if res := call(t, tool, map[string]any{}); res == nil || !res.IsError {
		t.Errorf("missing image_source must be a business error")
	}
}

func TestDescribeImageImageIDShortcut(t *testing.T) {
	// After the first call the original bytes are archived in L0, so a later
	// turn can pass the image_id alone and still resolve + hit the L2 cache.
	mock := vlm.NewMock("glm", "cached description via id")
	deps := newDeps(t, mock)
	tool := NewDescribeTool(deps)
	p := writeTestPNG(t, 200, 200)

	first := call(t, tool, map[string]any{"image_source": p})
	if first.IsError {
		t.Fatalf("first call failed: %s", resultText(first))
	}
	// The first result should expose the image_id (used by later turns).
	id := extractImageID(t, resultText(first))
	if id == "" {
		t.Fatalf("result did not expose an image_id: %s", resultText(first))
	}
	// Resolve via the bare image_id. L0 provides the bytes; L2 provides the
	// description. The VLM must NOT be hit again.
	byID := call(t, tool, map[string]any{"image_source": id})
	if byID.IsError {
		t.Fatalf("image_id call failed: %s", resultText(byID))
	}
	if !strings.Contains(resultText(byID), "cached description via id") {
		t.Errorf("image_id path must reuse the cached description: %s", resultText(byID))
	}
	if mock.CallCount() != 1 {
		t.Errorf("image_id lookup must not call the VLM again: count=%d", mock.CallCount())
	}
}

// extractImageID pulls the image_id value out of a tool result headline, which
// looks like "...[image_id=ABC...; focus=...; model=...; cache=...]".
func extractImageID(t *testing.T, text string) string {
	t.Helper()
	const prefix = "image_id="
	i := strings.Index(text, prefix)
	if i < 0 {
		return ""
	}
	rest := text[i+len(prefix):]
	if end := strings.IndexAny(rest, ";]"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}
