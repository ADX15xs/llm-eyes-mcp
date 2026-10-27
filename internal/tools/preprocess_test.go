package tools

import (
	"testing"

	"github.com/xiaos/llm-eyes-mcp/internal/imgproc"
	"github.com/xiaos/llm-eyes-mcp/internal/vlm"
)

// TestPreprocessL1HitKeepsDimensions: the L1-hit path must restore the
// processed dimensions. Coordinate parsing classifies pixel vs thousandths
// replies against the size the model actually saw — zero dims there silently
// force the thousandths branch and mislocate every pixel-coordinated box.
func TestPreprocessL1HitKeepsDimensions(t *testing.T) {
	deps, _ := newCompareDeps(t, vlm.NewMock("qwen", ""))
	p := writeCompareTestImage(t, 375, 812)
	img, err := deps.Loader.Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	fresh, err := deps.preprocess(img, imgproc.HardOptions())
	if err != nil {
		t.Fatalf("fresh preprocess: %v", err)
	}
	if fresh.Width != 375 || fresh.Height != 812 {
		t.Fatalf("fresh dims = %dx%d, want 375x812", fresh.Width, fresh.Height)
	}

	hit, err := deps.preprocess(img, imgproc.HardOptions())
	if err != nil {
		t.Fatalf("l1-hit preprocess: %v", err)
	}
	if hit.Width != fresh.Width || hit.Height != fresh.Height {
		t.Fatalf("L1-hit dims = %dx%d, want %dx%d", hit.Width, hit.Height, fresh.Width, fresh.Height)
	}
}
