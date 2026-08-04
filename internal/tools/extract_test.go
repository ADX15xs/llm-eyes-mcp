package tools

import (
	"strings"
	"testing"

	"github.com/xiaos/llm-eyes-mcp/internal/vlm"
)

// extract_test drives extract_text (OCR) end-to-end and proves L2 caching.

func TestExtractTextReturnsOCR(t *testing.T) {
	mock := vlm.NewMock("glm", "INVOICE #12345  TOTAL 99.00")
	deps := newDeps(t, mock)
	tool := NewExtractTool(deps)
	p := writeTestPNG(t, 240, 120)

	res := call(t, tool, map[string]any{"image_source": p})
	if res == nil {
		t.Fatal("extract returned nil")
	}
	if res.IsError {
		t.Fatalf("extract should succeed: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "12345") {
		t.Errorf("result should carry the OCR text: %s", resultText(res))
	}
	if mock.CallCount() != 1 {
		t.Errorf("extract must call the VLM once: count=%d", mock.CallCount())
	}
}

func TestExtractTextCachesAcrossCalls(t *testing.T) {
	mock := vlm.NewMock("glm", "LINE ONE\nLINE TWO")
	deps := newDeps(t, mock)
	tool := NewExtractTool(deps)
	p := writeTestPNG(t, 240, 120)

	if res := call(t, tool, map[string]any{"image_source": p}); res.IsError {
		t.Fatalf("first call failed: %s", resultText(res))
	}
	if mock.CallCount() != 1 {
		t.Fatalf("first call must hit VLM once: count=%d", mock.CallCount())
	}
	res2 := call(t, tool, map[string]any{"image_source": p})
	if res2.IsError {
		t.Fatalf("second call failed: %s", resultText(res2))
	}
	if mock.CallCount() != 1 {
		t.Errorf("repeat extract must be served from L2: VLM count=%d want 1", mock.CallCount())
	}
}

func TestExtractTextRefreshBypassesCache(t *testing.T) {
	mock := vlm.NewMock("glm", "fresh OCR")
	deps := newDeps(t, mock)
	tool := NewExtractTool(deps)
	p := writeTestPNG(t, 240, 120)

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

func TestExtractTextMissingSourceIsBusinessError(t *testing.T) {
	deps := newDeps(t, vlm.NewMock("glm", "x"))
	tool := NewExtractTool(deps)
	if res := call(t, tool, map[string]any{}); res == nil || !res.IsError {
		t.Errorf("missing image_source must be a business error")
	}
}

func TestExtractTextVLMFailureIsBusinessError(t *testing.T) {
	mock := vlm.NewFailingMock("glm", "ocr backend unavailable")
	deps := newDeps(t, mock)
	tool := NewExtractTool(deps)
	res := call(t, tool, map[string]any{"image_source": writeTestPNG(t, 240, 120)})
	if res == nil || !res.IsError {
		t.Errorf("VLM failure must become a business error result")
	}
}
