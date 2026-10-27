package imgproc

import (
	"image"
	"image/color"
	"math"
	"testing"
)

func fillRect(t *testing.T, img *image.RGBA, rect image.Rectangle, c color.RGBA) {
	t.Helper()
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func TestAverageRGBPureRect(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	fillRect(t, img, image.Rect(0, 0, 40, 40), color.RGBA{R: 0, G: 0, B: 255, A: 255})
	fillRect(t, img, image.Rect(10, 10, 30, 30), color.RGBA{R: 255, G: 102, B: 0, A: 255}) // #FF6600

	got := AverageRGB(img, image.Rect(10, 10, 30, 30), 0.2)
	if d := got.Delta(RGB{R: 255, G: 102, B: 0}); d > 0.5 {
		t.Errorf("AverageRGB inset = %v (%s), want pure #FF6600", got, got.Hex())
	}
}

func TestAverageRGBInsetRejectsEdgeContamination(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	fillRect(t, img, image.Rect(0, 0, 40, 40), color.RGBA{R: 0, G: 0, B: 255, A: 255})
	fillRect(t, img, image.Rect(10, 10, 30, 30), color.RGBA{R: 255, G: 102, B: 0, A: 255})
	// A 1px blue stripe inside the red rect's left edge.
	fillRect(t, img, image.Rect(10, 10, 12, 30), color.RGBA{R: 0, G: 0, B: 255, A: 255})

	withInset := AverageRGB(img, image.Rect(10, 10, 30, 30), 0.2)
	noInset := AverageRGB(img, image.Rect(10, 10, 30, 30), 0)

	if d := withInset.Delta(RGB{R: 255, G: 102, B: 0}); d > 0.5 {
		t.Errorf("inset sample = %s, want uncontaminated #FF6600", withInset.Hex())
	}
	if noInset.Delta(withInset) < 1 {
		t.Errorf("inset should differ from raw average: %v vs %v", noInset, withInset)
	}
}

func TestAverageRGBEmptyAndClamping(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	if got := AverageRGB(img, image.Rect(100, 100, 200, 200), 0); got != (RGB{}) {
		t.Errorf("out-of-bounds rect should yield zero RGB, got %v", got)
	}
	// Rect partially outside is clamped to the intersection.
	got := AverageRGB(img, image.Rect(-5, -5, 5, 5), 0)
	if got != (RGB{R: 0, G: 0, B: 0}) {
		t.Errorf("clamped black rect should be #000000, got %s", got.Hex())
	}
}

func TestRGBDeltaAndHex(t *testing.T) {
	a := RGB{R: 255, G: 0, B: 0}
	b := RGB{R: 255, G: 102, B: 0}
	if d := a.Delta(b); math.Abs(d-102) > 0.01 {
		t.Errorf("Delta = %v, want 102", d)
	}
	if got := b.Hex(); got != "#FF6600" {
		t.Errorf("Hex = %s, want #FF6600", got)
	}
	if got := (RGB{R: -3, G: 300, B: 12.6}).Hex(); got != "#00FF0D" {
		t.Errorf("Hex clamping = %s, want #00FF0D", got)
	}
}
