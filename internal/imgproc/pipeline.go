package imgproc

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	xdraw "golang.org/x/image/draw"
)

// Pipeline selects the preprocessing profile. The choice is driven by the tool
// the agent called, never guessed from the image itself.
type Pipeline string

const (
	// PipelineHard serves measurement and OCR: preserve edges at all costs.
	PipelineHard Pipeline = "hard"
	// PipelineSoft serves aesthetics and scene understanding: low-frequency
	// semantics survive aggressive compression.
	PipelineSoft Pipeline = "soft"
)

// Defaults for the two profiles.
const (
	HardMaxEdge   = 1500 // longest side, lossless
	SoftShortEdge = 512  // shortest side, lossy
	SoftJPEGQual  = 85
)

// SchemaVersion identifies the preprocessing contract. Bump it whenever the
// output of Process changes for the same input, so L1 entries written by an
// older build are ignored instead of silently reused.
const SchemaVersion = "v1"

// Options tunes one preprocessing run.
type Options struct {
	Pipeline Pipeline
	// EnableCLAHE is on by default per the preprocessing spec.
	EnableCLAHE bool
	// EnableSharpen applies unsharp masking; useful for hard pipelines only.
	EnableSharpen bool
	HardMaxEdge   int
	SoftShortEdge int
	JPEGQuality   int
	CLAHETiles    int
	CLAHEClip     float64
}

// HardOptions returns the measurement/OCR profile.
func HardOptions() Options {
	return Options{
		Pipeline:      PipelineHard,
		EnableCLAHE:   true,
		EnableSharpen: true,
		HardMaxEdge:   HardMaxEdge,
		CLAHETiles:    DefaultCLAHETiles,
		CLAHEClip:     DefaultClipLimit,
	}
}

// SoftOptions returns the perception profile.
func SoftOptions() Options {
	return Options{
		Pipeline:      PipelineSoft,
		EnableCLAHE:   true,
		EnableSharpen: false,
		SoftShortEdge: SoftShortEdge,
		JPEGQuality:   SoftJPEGQual,
		CLAHETiles:    DefaultCLAHETiles,
		CLAHEClip:     DefaultClipLimit,
	}
}

// Output is the encoded, VLM-ready image.
type Output struct {
	Data     []byte
	MIMEType string
	Width    int
	Height   int
	// SourceWidth/SourceHeight are the ORIGINAL dimensions. Normalised VLM
	// coordinates must be scaled by these, not by the resized dimensions, so
	// every measurement is reported in original-image pixels.
	SourceWidth  int
	SourceHeight int
	Resized      bool
	Pipeline     Pipeline
}

// CacheTag is a short, stable descriptor of the applied transform. It goes into
// the L1 cache key so hard and soft variants of the same MD5 never collide.
func (o Options) CacheTag() string {
	p := string(o.Pipeline)
	edge := o.HardMaxEdge
	q := 0
	if o.Pipeline == PipelineSoft {
		edge = o.SoftShortEdge
		q = o.JPEGQuality
	}
	return fmt.Sprintf("%s-e%d-q%d-c%t-s%t-%s", p, edge, q, o.EnableCLAHE, o.EnableSharpen, SchemaVersion)
}

// Process decodes, rescales, enhances and re-encodes an image for the VLM.
func Process(raw []byte, opts Options) (*Output, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW == 0 || srcH == 0 {
		return nil, fmt.Errorf("decode image: zero dimensions")
	}

	targetW, targetH := targetSize(srcW, srcH, opts)
	work := src
	resized := false
	if targetW != srcW || targetH != srcH {
		dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
		// CatmullRom keeps small text and thin edges legible when downscaling;
		// nearest/bilinear visibly smear them.
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
		work = dst
		resized = true
	}

	if opts.EnableCLAHE {
		work = CLAHE(work, opts.CLAHETiles, opts.CLAHETiles, opts.CLAHEClip)
	}
	if opts.EnableSharpen {
		work = UnsharpMask(work, 1, 0.6)
	}

	var buf bytes.Buffer
	mime := "image/png"
	if opts.Pipeline == PipelineSoft {
		q := opts.JPEGQuality
		if q <= 0 || q > 100 {
			q = SoftJPEGQual
		}
		if err := jpeg.Encode(&buf, work, &jpeg.Options{Quality: q}); err != nil {
			return nil, fmt.Errorf("encode jpeg: %w", err)
		}
		mime = "image/jpeg"
	} else {
		// Lossless only. JPEG ringing artefacts around high-contrast edges
		// directly corrupt the coordinates the hard pipeline depends on.
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, work); err != nil {
			return nil, fmt.Errorf("encode png: %w", err)
		}
	}

	return &Output{
		Data:         buf.Bytes(),
		MIMEType:     mime,
		Width:        targetW,
		Height:       targetH,
		SourceWidth:  srcW,
		SourceHeight: srcH,
		Resized:      resized,
		Pipeline:     opts.Pipeline,
	}, nil
}

// targetSize computes the post-scaling dimensions. Images are only ever scaled
// down: upscaling adds no information and wastes VLM patches.
func targetSize(w, h int, opts Options) (int, int) {
	switch opts.Pipeline {
	case PipelineSoft:
		limit := opts.SoftShortEdge
		if limit <= 0 {
			limit = SoftShortEdge
		}
		short := minInt(w, h)
		if short <= limit {
			return w, h
		}
		ratio := float64(limit) / float64(short)
		return scaleDims(w, h, ratio)
	default:
		limit := opts.HardMaxEdge
		if limit <= 0 {
			limit = HardMaxEdge
		}
		long := maxInt(w, h)
		if long <= limit {
			return w, h
		}
		ratio := float64(limit) / float64(long)
		return scaleDims(w, h, ratio)
	}
}

func scaleDims(w, h int, ratio float64) (int, int) {
	nw := maxInt(1, int(float64(w)*ratio+0.5))
	nh := maxInt(1, int(float64(h)*ratio+0.5))
	return nw, nh
}
