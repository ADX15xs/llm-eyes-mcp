package imgproc

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// gradientPNG produces a low-contrast image: every channel sits in a narrow
// band, which is exactly what CLAHE is supposed to stretch.
func gradientPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := uint8(110 + (x*10)/maxInt(1, w) + (y*10)/maxInt(1, h))
			img.Set(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return encodePNG(t, img)
}

// checkerPNG has hard black/white edges, useful for sharpening assertions.
func checkerPNG(t *testing.T, w, h, cell int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := uint8(0)
			if ((x/cell)+(y/cell))%2 == 0 {
				v = 255
			}
			img.Set(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return encodePNG(t, img)
}

// noisyPNG approximates the entropy of a real photograph, so byte-size
// assertions are not distorted by PNG's affinity for synthetic gradients.
func noisyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(42)) // fixed seed: fixtures must be reproducible
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(rng.Intn(256)),
				G: uint8(rng.Intn(256)),
				B: uint8(rng.Intn(256)),
				A: 255,
			})
		}
	}
	return encodePNG(t, img)
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func decode(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return img
}

// ---------------------------------------------------------------------------
// targetSize - the scaling contract
// ---------------------------------------------------------------------------

func TestTargetSizeHardPipeline(t *testing.T) {
	opts := HardOptions()
	cases := []struct {
		name         string
		w, h         int
		wantW, wantH int
	}{
		{"smaller than the limit is untouched", 800, 600, 800, 600},
		{"exactly at the limit is untouched", 1500, 900, 1500, 900},
		{"landscape capped on the long side", 3000, 1500, 1500, 750},
		{"portrait capped on the long side", 1500, 3000, 750, 1500},
		{"square capped", 3000, 3000, 1500, 1500},
		{"extreme aspect ratio keeps at least 1px", 30000, 3, 1500, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, h := targetSize(tc.w, tc.h, opts)
			if w != tc.wantW || h != tc.wantH {
				t.Errorf("targetSize(%d,%d) = %dx%d, want %dx%d", tc.w, tc.h, w, h, tc.wantW, tc.wantH)
			}
		})
	}
}

func TestTargetSizeSoftPipeline(t *testing.T) {
	opts := SoftOptions()
	cases := []struct {
		name         string
		w, h         int
		wantW, wantH int
	}{
		{"smaller than the limit is untouched", 400, 300, 400, 300},
		{"landscape capped on the short side", 2048, 1024, 1024, 512},
		{"portrait capped on the short side", 1024, 2048, 512, 1024},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, h := targetSize(tc.w, tc.h, opts)
			if w != tc.wantW || h != tc.wantH {
				t.Errorf("targetSize(%d,%d) = %dx%d, want %dx%d", tc.w, tc.h, w, h, tc.wantW, tc.wantH)
			}
		})
	}
}

// Upscaling adds no information and wastes VLM patches.
func TestNeverUpscales(t *testing.T) {
	for _, opts := range []Options{HardOptions(), SoftOptions()} {
		w, h := targetSize(64, 48, opts)
		if w > 64 || h > 48 {
			t.Errorf("%s pipeline upscaled 64x48 to %dx%d", opts.Pipeline, w, h)
		}
	}
}

func TestTargetSizePreservesAspectRatio(t *testing.T) {
	w, h := targetSize(4000, 2250, HardOptions()) // 16:9
	got := float64(w) / float64(h)
	if math.Abs(got-16.0/9.0) > 0.01 {
		t.Errorf("aspect ratio drifted: %dx%d = %.4f", w, h, got)
	}
}

// ---------------------------------------------------------------------------
// Process
// ---------------------------------------------------------------------------

func TestProcessHardIsLosslessPNG(t *testing.T) {
	raw := checkerPNG(t, 200, 100, 10)
	out, err := Process(raw, HardOptions())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if out.MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want image/png - JPEG ringing corrupts measurement", out.MIMEType)
	}
	if out.Pipeline != PipelineHard {
		t.Errorf("Pipeline = %q", out.Pipeline)
	}
	if out.SourceWidth != 200 || out.SourceHeight != 100 {
		t.Errorf("source dims = %dx%d, want 200x100", out.SourceWidth, out.SourceHeight)
	}
	if out.Resized {
		t.Error("a 200x100 image is under the 1500 limit and must not be resized")
	}
	if out.Width != 200 || out.Height != 100 {
		t.Errorf("output dims = %dx%d", out.Width, out.Height)
	}
	// It must really be a PNG on the wire.
	if _, err := png.Decode(bytes.NewReader(out.Data)); err != nil {
		t.Errorf("output is not a valid PNG: %v", err)
	}
}

func TestProcessSoftIsJPEG(t *testing.T) {
	// A noisy source, so the payload comparison reflects a real photograph
	// rather than a synthetic gradient that PNG compresses to almost nothing.
	raw := noisyPNG(t, 1024, 768)
	out, err := Process(raw, SoftOptions())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if out.MIMEType != "image/jpeg" {
		t.Errorf("MIMEType = %q, want image/jpeg", out.MIMEType)
	}
	if !out.Resized {
		t.Error("768 short edge must be scaled down to 512")
	}
	if out.Height != 512 {
		t.Errorf("short edge = %d, want 512", out.Height)
	}
	if out.Width != 683 {
		t.Errorf("width = %d, want 683 (aspect ratio preserved)", out.Width)
	}
	if _, err := jpeg.Decode(bytes.NewReader(out.Data)); err != nil {
		t.Errorf("output is not a valid JPEG: %v", err)
	}
	// The whole point of the soft pipeline is a much cheaper payload.
	if len(out.Data) >= len(raw) {
		t.Errorf("soft output (%d B) is not smaller than the source (%d B)", len(out.Data), len(raw))
	}
	// 768 -> 512 on the short edge is a 1.5x linear, 2.25x area reduction.
	if srcPixels, outPixels := 1024*768, out.Width*out.Height; outPixels*2 > srcPixels {
		t.Errorf("pixel count only fell from %d to %d - the VLM patch budget is not being saved", srcPixels, outPixels)
	}
}

// Source dimensions must survive resizing: normalised VLM coordinates are
// scaled by the ORIGINAL size so every number is reported in source pixels.
func TestProcessKeepsOriginalDimensions(t *testing.T) {
	raw := gradientPNG(t, 3000, 2000)
	out, err := Process(raw, HardOptions())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if out.SourceWidth != 3000 || out.SourceHeight != 2000 {
		t.Fatalf("source dims = %dx%d, want 3000x2000", out.SourceWidth, out.SourceHeight)
	}
	if out.Width != 1500 || out.Height != 1000 {
		t.Errorf("processed dims = %dx%d, want 1500x1000", out.Width, out.Height)
	}
	if !out.Resized {
		t.Error("Resized = false after a 2x downscale")
	}
	// The decoded payload must actually match the reported dimensions.
	img := decode(t, out.Data)
	if img.Bounds().Dx() != out.Width || img.Bounds().Dy() != out.Height {
		t.Errorf("payload is %dx%d but Output says %dx%d",
			img.Bounds().Dx(), img.Bounds().Dy(), out.Width, out.Height)
	}
}

func TestProcessRejectsGarbage(t *testing.T) {
	if _, err := Process([]byte("not an image"), HardOptions()); err == nil {
		t.Error("want a decode error")
	}
	if _, err := Process(nil, HardOptions()); err == nil {
		t.Error("want an error for nil input")
	}
}

func TestProcessAcceptsJPEGInput(t *testing.T) {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 4), G: 100, B: uint8(y * 4), A: 255})
		}
	}
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	out, err := Process(buf.Bytes(), HardOptions())
	if err != nil {
		t.Fatalf("Process(jpeg): %v", err)
	}
	if out.MIMEType != "image/png" {
		t.Errorf("a JPEG input must still leave the hard pipeline as PNG, got %q", out.MIMEType)
	}
}

func TestProcessTinyImage(t *testing.T) {
	out, err := Process(checkerPNG(t, 1, 1, 1), HardOptions())
	if err != nil {
		t.Fatalf("1x1 image failed: %v", err)
	}
	if out.Width != 1 || out.Height != 1 {
		t.Errorf("dims = %dx%d", out.Width, out.Height)
	}
}

func TestProcessIsDeterministic(t *testing.T) {
	raw := checkerPNG(t, 128, 128, 8)
	a, err := Process(raw, HardOptions())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Process(raw, HardOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Data, b.Data) {
		t.Error("Process is not deterministic - the L1 cache would be useless")
	}
}

func TestProcessInvalidJPEGQualityFallsBack(t *testing.T) {
	opts := SoftOptions()
	opts.JPEGQuality = 999
	out, err := Process(gradientPNG(t, 100, 100), opts)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(out.Data)); err != nil {
		t.Errorf("out-of-range quality produced an invalid JPEG: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CacheTag - hard and soft variants must never collide in L1
// ---------------------------------------------------------------------------

func TestCacheTagDistinguishesPipelines(t *testing.T) {
	hard, soft := HardOptions().CacheTag(), SoftOptions().CacheTag()
	if hard == soft {
		t.Fatal("hard and soft share a cache tag - one would overwrite the other")
	}
	if !strings.HasPrefix(hard, "hard-") || !strings.HasPrefix(soft, "soft-") {
		t.Errorf("tags = %q / %q", hard, soft)
	}
	// The schema version must be embedded so old entries are ignored.
	if !strings.HasSuffix(hard, SchemaVersion) || !strings.HasSuffix(soft, SchemaVersion) {
		t.Errorf("tags do not carry the schema version: %q / %q", hard, soft)
	}
}

func TestCacheTagReactsToEveryKnob(t *testing.T) {
	base := HardOptions()
	baseTag := base.CacheTag()

	variants := map[string]func(*Options){
		"max edge":    func(o *Options) { o.HardMaxEdge = 1000 },
		"clahe off":   func(o *Options) { o.EnableCLAHE = false },
		"sharpen off": func(o *Options) { o.EnableSharpen = false },
	}
	for name, mutate := range variants {
		t.Run(name, func(t *testing.T) {
			o := HardOptions()
			mutate(&o)
			if o.CacheTag() == baseTag {
				t.Errorf("%s did not change the cache tag (%q)", name, baseTag)
			}
		})
	}

	softBase := SoftOptions().CacheTag()
	o := SoftOptions()
	o.JPEGQuality = 60
	if o.CacheTag() == softBase {
		t.Error("jpeg quality did not change the cache tag")
	}
}

func TestCacheTagIsStable(t *testing.T) {
	if HardOptions().CacheTag() != HardOptions().CacheTag() {
		t.Error("cache tag is not stable across calls")
	}
}

// ---------------------------------------------------------------------------
// CLAHE
// ---------------------------------------------------------------------------

func TestCLAHEIncreasesContrast(t *testing.T) {
	src := decode(t, gradientPNG(t, 256, 256))
	before := stdDevLuma(src)

	out := CLAHE(src, DefaultCLAHETiles, DefaultCLAHETiles, DefaultClipLimit)
	after := stdDevLuma(out)

	if after <= before {
		t.Errorf("luma std dev went from %.2f to %.2f - CLAHE did not stretch contrast", before, after)
	}
}

func TestCLAHEPreservesDimensions(t *testing.T) {
	src := decode(t, gradientPNG(t, 123, 77))
	out := CLAHE(src, 8, 8, 2.0)
	if out.Bounds().Dx() != 123 || out.Bounds().Dy() != 77 {
		t.Errorf("dims = %dx%d, want 123x77", out.Bounds().Dx(), out.Bounds().Dy())
	}
}

func TestCLAHEHandlesImagesSmallerThanTheTileGrid(t *testing.T) {
	// 4x4 image with an 8x8 tile grid: tiles would be zero-sized if unguarded.
	src := decode(t, checkerPNG(t, 4, 4, 1))
	out := CLAHE(src, 8, 8, 2.0)
	if out.Bounds().Dx() != 4 || out.Bounds().Dy() != 4 {
		t.Errorf("dims = %v", out.Bounds())
	}
}

func TestCLAHEHandlesUniformImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: 128, G: 128, B: 128, A: 255})
		}
	}
	out := CLAHE(img, 8, 8, 2.0)
	if out == nil {
		t.Fatal("nil output")
	}
	// A flat image must not blow up into noise.
	if sd := stdDevLuma(out); sd > 40 {
		t.Errorf("a uniform image became noisy: std dev %.2f", sd)
	}
}

func TestCLAHEIsDeterministic(t *testing.T) {
	src := decode(t, gradientPNG(t, 64, 64))
	a := encodePNG(t, CLAHE(src, 8, 8, 2.0))
	b := encodePNG(t, CLAHE(src, 8, 8, 2.0))
	if !bytes.Equal(a, b) {
		t.Error("CLAHE is not deterministic")
	}
}

// ---------------------------------------------------------------------------
// UnsharpMask
// ---------------------------------------------------------------------------

func TestUnsharpMaskAmplifiesEdges(t *testing.T) {
	src := decode(t, checkerPNG(t, 128, 128, 16))
	before := edgeEnergy(src)

	out := UnsharpMask(src, 1, 0.6)
	after := edgeEnergy(out)

	if after < before {
		t.Errorf("edge energy dropped from %.2f to %.2f - sharpening made things blurrier", before, after)
	}
}

func TestUnsharpMaskPreservesDimensions(t *testing.T) {
	src := decode(t, checkerPNG(t, 51, 33, 4))
	out := UnsharpMask(src, 1, 0.6)
	if out.Bounds().Dx() != 51 || out.Bounds().Dy() != 33 {
		t.Errorf("dims = %v", out.Bounds())
	}
}

func TestUnsharpMaskZeroAmountIsANoOp(t *testing.T) {
	src := decode(t, checkerPNG(t, 32, 32, 4))
	out := UnsharpMask(src, 1, 0)
	if !bytes.Equal(encodePNG(t, src), encodePNG(t, out)) {
		// A zero amount should leave the image visually unchanged; allow a
		// small numeric drift from the RGBA round-trip but not a real change.
		if diff := meanAbsDiff(src, out); diff > 1.0 {
			t.Errorf("amount=0 changed the image (mean abs diff %.3f)", diff)
		}
	}
}

func TestUnsharpMaskDoesNotOverflow(t *testing.T) {
	// Pure white next to pure black is where naive sharpening wraps around.
	src := decode(t, checkerPNG(t, 64, 64, 1))
	out := UnsharpMask(src, 1, 2.0)
	b := out.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := out.At(x, y).RGBA()
			for _, c := range []uint32{r, g, bl} {
				if c>>8 > 255 {
					t.Fatalf("channel overflow at (%d,%d): %d", x, y, c>>8)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// measurement helpers
// ---------------------------------------------------------------------------

func luma(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	return 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
}

func stdDevLuma(img image.Image) float64 {
	b := img.Bounds()
	n := 0.0
	sum, sumSq := 0.0, 0.0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			v := luma(img.At(x, y))
			sum += v
			sumSq += v * v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	mean := sum / n
	return math.Sqrt(math.Max(0, sumSq/n-mean*mean))
}

// edgeEnergy is the mean absolute horizontal+vertical gradient.
func edgeEnergy(img image.Image) float64 {
	b := img.Bounds()
	total, n := 0.0, 0.0
	for y := b.Min.Y; y < b.Max.Y-1; y++ {
		for x := b.Min.X; x < b.Max.X-1; x++ {
			c := luma(img.At(x, y))
			total += math.Abs(luma(img.At(x+1, y))-c) + math.Abs(luma(img.At(x, y+1))-c)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return total / n
}

func meanAbsDiff(a, b image.Image) float64 {
	bounds := a.Bounds()
	total, n := 0.0, 0.0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			total += math.Abs(luma(a.At(x, y)) - luma(b.At(x, y)))
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return total / n
}
