// Package imgproc implements the image preprocessing pipelines in pure Go.
// There is deliberately no OpenCV/GoCV dependency: GoCV drags in ~500MB of CGO
// build artefacts, which would destroy the "single <20MB binary" promise.
package imgproc

import (
	"image"
	"image/color"
	"math"
)

// DefaultCLAHETiles is the grid resolution used for adaptive equalisation.
const DefaultCLAHETiles = 8

// DefaultClipLimit bounds contrast amplification. Values around 2-3 sharpen
// faint document scans without blowing out noise.
const DefaultClipLimit = 2.0

// CLAHE applies Contrast Limited Adaptive Histogram Equalisation to the
// luminance channel, leaving chroma untouched so colours do not shift.
//
// The image is split into tilesX*tilesY tiles; each tile gets its own clipped
// histogram equalisation LUT, and per-pixel results are bilinearly interpolated
// between the four surrounding tile LUTs. Without that interpolation the output
// shows visible tile seams.
func CLAHE(src image.Image, tilesX, tilesY int, clipLimit float64) *image.RGBA {
	if tilesX < 1 {
		tilesX = DefaultCLAHETiles
	}
	if tilesY < 1 {
		tilesY = DefaultCLAHETiles
	}
	if clipLimit <= 0 {
		clipLimit = DefaultClipLimit
	}

	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == 0 || h == 0 {
		return dst
	}
	// Tiles must contain enough samples for a histogram to mean anything.
	if w < tilesX*4 {
		tilesX = maxInt(1, w/4)
	}
	if h < tilesY*4 {
		tilesY = maxInt(1, h/4)
	}

	// Split into Y/Cb/Cr planes.
	yPlane := make([]uint8, w*h)
	cbPlane := make([]uint8, w*h)
	crPlane := make([]uint8, w*h)
	alpha := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r16, g16, b16, a16 := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			i := y*w + x
			yy, cb, cr := color.RGBToYCbCr(uint8(r16>>8), uint8(g16>>8), uint8(b16>>8))
			yPlane[i], cbPlane[i], crPlane[i], alpha[i] = yy, cb, cr, uint8(a16>>8)
		}
	}

	tileW := (w + tilesX - 1) / tilesX
	tileH := (h + tilesY - 1) / tilesY

	// One 256-entry LUT per tile.
	luts := make([][256]uint8, tilesX*tilesY)
	for ty := 0; ty < tilesY; ty++ {
		for tx := 0; tx < tilesX; tx++ {
			x0, y0 := tx*tileW, ty*tileH
			x1, y1 := minInt(x0+tileW, w), minInt(y0+tileH, h)
			luts[ty*tilesX+tx] = tileLUT(yPlane, w, x0, y0, x1, y1, clipLimit)
		}
	}

	// Bilinear blend of the four neighbouring tile LUTs.
	for y := 0; y < h; y++ {
		fy := float64(y)/float64(tileH) - 0.5
		ty0, wy := neighbour(fy, tilesY)
		ty1 := minInt(ty0+1, tilesY-1)
		for x := 0; x < w; x++ {
			fx := float64(x)/float64(tileW) - 0.5
			tx0, wx := neighbour(fx, tilesX)
			tx1 := minInt(tx0+1, tilesX-1)

			i := y*w + x
			v := yPlane[i]
			v00 := float64(luts[ty0*tilesX+tx0][v])
			v01 := float64(luts[ty0*tilesX+tx1][v])
			v10 := float64(luts[ty1*tilesX+tx0][v])
			v11 := float64(luts[ty1*tilesX+tx1][v])

			top := v00*(1-wx) + v01*wx
			bot := v10*(1-wx) + v11*wx
			blended := top*(1-wy) + bot*wy

			r, g, bl := color.YCbCrToRGB(uint8(clampF(blended, 0, 255)), cbPlane[i], crPlane[i])
			o := i * 4
			dst.Pix[o+0] = r
			dst.Pix[o+1] = g
			dst.Pix[o+2] = bl
			dst.Pix[o+3] = alpha[i]
		}
	}
	return dst
}

// neighbour returns the lower tile index and the interpolation weight toward
// the next tile, with edge tiles clamped (weight 0) to avoid wrap-around.
func neighbour(f float64, tiles int) (int, float64) {
	if f <= 0 {
		return 0, 0
	}
	idx := int(math.Floor(f))
	if idx >= tiles-1 {
		return tiles - 1, 0
	}
	return idx, f - float64(idx)
}

// tileLUT builds a clipped, equalised lookup table for one tile.
func tileLUT(yPlane []uint8, stride, x0, y0, x1, y1 int, clipLimit float64) [256]uint8 {
	var hist [256]int
	n := 0
	for y := y0; y < y1; y++ {
		row := y * stride
		for x := x0; x < x1; x++ {
			hist[yPlane[row+x]]++
			n++
		}
	}
	var lut [256]uint8
	if n == 0 {
		for i := range lut {
			lut[i] = uint8(i)
		}
		return lut
	}

	// Clip the histogram and redistribute the excess uniformly. This is the
	// "contrast limited" part: without it, flat regions get amplified noise.
	limit := int(math.Max(1, clipLimit*float64(n)/256.0))
	excess := 0
	for i := range hist {
		if hist[i] > limit {
			excess += hist[i] - limit
			hist[i] = limit
		}
	}
	if excess > 0 {
		share := excess / 256
		rem := excess % 256
		for i := range hist {
			hist[i] += share
		}
		for i := 0; i < rem; i++ {
			hist[i*256/rem]++
		}
	}

	// Cumulative distribution -> LUT.
	cum := 0
	for i := range hist {
		cum += hist[i]
		lut[i] = uint8(clampF(float64(cum)*255.0/float64(n), 0, 255))
	}
	return lut
}

// UnsharpMask sharpens the image: out = src + amount * (src - blur(src)).
// Used on hard-pipeline images so the VLM sees crisper part edges.
func UnsharpMask(src image.Image, radius int, amount float64) *image.RGBA {
	if radius < 1 {
		radius = 1
	}
	if amount <= 0 {
		amount = 1.0
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	base := toRGBA(src)
	blur := gaussianBlur(base, radius)

	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(base.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			o := float64(base.Pix[i+c])
			bl := float64(blur.Pix[i+c])
			dst.Pix[i+c] = uint8(clampF(o+amount*(o-bl), 0, 255))
		}
		dst.Pix[i+3] = base.Pix[i+3]
	}
	return dst
}

// gaussianBlur runs a separable Gaussian convolution (two 1-D passes), which is
// O(n*r) instead of the O(n*r²) of a naive 2-D kernel.
func gaussianBlur(src *image.RGBA, radius int) *image.RGBA {
	sigma := float64(radius) / 2.0
	if sigma <= 0 {
		sigma = 0.5
	}
	size := radius*2 + 1
	kernel := make([]float64, size)
	sum := 0.0
	for i := range kernel {
		d := float64(i - radius)
		kernel[i] = math.Exp(-(d * d) / (2 * sigma * sigma))
		sum += kernel[i]
	}
	for i := range kernel {
		kernel[i] /= sum
	}

	w := src.Bounds().Dx()
	h := src.Bounds().Dy()
	tmp := image.NewRGBA(src.Bounds())
	dst := image.NewRGBA(src.Bounds())

	// Horizontal pass.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc [4]float64
			for k := -radius; k <= radius; k++ {
				sx := clampInt(x+k, 0, w-1)
				o := y*src.Stride + sx*4
				wt := kernel[k+radius]
				acc[0] += float64(src.Pix[o+0]) * wt
				acc[1] += float64(src.Pix[o+1]) * wt
				acc[2] += float64(src.Pix[o+2]) * wt
				acc[3] += float64(src.Pix[o+3]) * wt
			}
			o := y*tmp.Stride + x*4
			for c := 0; c < 4; c++ {
				tmp.Pix[o+c] = uint8(clampF(acc[c], 0, 255))
			}
		}
	}
	// Vertical pass.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc [4]float64
			for k := -radius; k <= radius; k++ {
				sy := clampInt(y+k, 0, h-1)
				o := sy*tmp.Stride + x*4
				wt := kernel[k+radius]
				acc[0] += float64(tmp.Pix[o+0]) * wt
				acc[1] += float64(tmp.Pix[o+1]) * wt
				acc[2] += float64(tmp.Pix[o+2]) * wt
				acc[3] += float64(tmp.Pix[o+3]) * wt
			}
			o := y*dst.Stride + x*4
			for c := 0; c < 4; c++ {
				dst.Pix[o+c] = uint8(clampF(acc[c], 0, 255))
			}
		}
	}
	return dst
}

func toRGBA(src image.Image) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		return r
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			o := y*dst.Stride + x*4
			dst.Pix[o+0] = uint8(r >> 8)
			dst.Pix[o+1] = uint8(g >> 8)
			dst.Pix[o+2] = uint8(bl >> 8)
			dst.Pix[o+3] = uint8(a >> 8)
		}
	}
	return dst
}

func clampF(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
