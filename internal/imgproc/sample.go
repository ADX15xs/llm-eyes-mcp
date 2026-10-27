package imgproc

import (
	"fmt"
	"image"
	"math"
)

// RGB is a mean colour sample with 0-255 components.
type RGB struct{ R, G, B float64 }

// Delta is the euclidean distance between two samples, 0 (identical) to ~441.7.
func (c RGB) Delta(o RGB) float64 {
	dr, dg, db := c.R-o.R, c.G-o.G, c.B-o.B
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

// Hex renders the sample as #RRGGBB.
func (c RGB) Hex() string {
	to := func(v float64) int {
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		return int(v + 0.5)
	}
	return fmt.Sprintf("#%02X%02X%02X", to(c.R), to(c.G), to(c.B))
}

// AverageRGB computes the mean colour of rect within src. inset (0..0.45)
// shrinks the rect toward its centre first, so anti-aliased edges and
// neighbouring content do not dilute the sample; rects smaller than 5x5 after
// shrinking are sampled as-is.
//
// Note: Go's At().RGBA() returns alpha-premultiplied values. Screenshots are
// opaque, so the straight 16-bit -> 8-bit conversion is exact there; heavily
// transparent sources would read darker than compositing would show.
func AverageRGB(src image.Image, rect image.Rectangle, inset float64) RGB {
	b := src.Bounds()
	r := rect.Intersect(b)
	if r.Empty() {
		return RGB{}
	}
	if inset > 0 && inset < 0.45 {
		dx := int(float64(r.Dx()) * inset)
		dy := int(float64(r.Dy()) * inset)
		shrunk := image.Rect(r.Min.X+dx, r.Min.Y+dy, r.Max.X-dx, r.Max.Y-dy)
		// Keep at least a 5x5 window; below that edge contamination is
		// unavoidable anyway and shrinking would degenerate.
		if shrunk.Dx() >= 5 && shrunk.Dy() >= 5 {
			r = shrunk
		}
	}

	var sumR, sumG, sumB float64
	var n float64
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := src.At(x, y).RGBA()
			sumR += float64(cr >> 8)
			sumG += float64(cg >> 8)
			sumB += float64(cb >> 8)
			n++
		}
	}
	if n == 0 {
		return RGB{}
	}
	return RGB{R: sumR / n, G: sumG / n, B: sumB / n}
}
