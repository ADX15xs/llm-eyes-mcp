package probe

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"golang.org/x/image/font/basicfont"
)

// TestDrawTextBoxMatchesGlyphPixels pins the ground-truth contract: the box
// drawText returns is the glyph cell from the font's advance grid (deterministic,
// independent of what is drawn) and it must fully contain the ink — the ground
// truth can never miss pixels that are visible on screen.
func TestDrawTextBoxMatchesGlyphPixels(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 300, 40))
	text := "Expires 2026-11-30"
	box := drawText(img, 10, 10, text, color.RGBA{0, 0, 0, 0xff})

	want := image.Rect(10, 10, 10+basicfont.Face7x13.Advance*len(text), 10+basicfont.Face7x13.Height)
	if box != want {
		t.Errorf("drawText box = %v, want %v", box, want)
	}

	ink := image.Rectangle{}
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			if img.RGBAAt(x, y).A == 0 {
				continue
			}
			ink = ink.Union(image.Rect(x, y, x+1, y+1))
		}
	}
	if ink.Empty() {
		t.Fatal("nothing was drawn")
	}
	if !ink.In(box) {
		t.Errorf("ink %v escapes ground-truth box %v", ink, box)
	}
}

func TestRenderGroundTruth(t *testing.T) {
	c, err := Render()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(c.GT); got != len(Targets) {
		t.Fatalf("gt entries = %d, want %d", got, len(Targets))
	}
	if _, err := png.Decode(bytes.NewReader(c.PNG)); err != nil {
		t.Fatalf("PNG does not decode: %v", err)
	}
	for _, tgt := range Targets {
		box, ok := c.GT[tgt.ID]
		if !ok {
			t.Fatalf("missing GT for %s", tgt.ID)
		}
		if box[0] < 0 || box[1] < 0 || box[2] > 1 || box[3] > 1 || box[0] >= box[2] || box[1] >= box[3] {
			t.Errorf("%s GT %v is outside [0,1] or inverted", tgt.ID, box)
		}
	}
	// Ambiguous truth would corrupt IoU scoring: ground-truth boxes must be
	// pairwise disjoint.
	for i := 0; i < len(Targets); i++ {
		for j := i + 1; j < len(Targets); j++ {
			a, b := c.GT[Targets[i].ID], c.GT[Targets[j].ID]
			if a[0] < b[2] && b[0] < a[2] && a[1] < b[3] && b[1] < a[3] {
				t.Errorf("GT %s %v overlaps %s %v", Targets[i].ID, a, Targets[j].ID, b)
			}
		}
	}
}
