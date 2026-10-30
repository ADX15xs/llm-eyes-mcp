package probe

import (
	"strings"

	"github.com/xiaos/llm-eyes-mcp/internal/geometry"
)

// Reply is the JSON schema the probe's system prompt demands.
type Reply struct {
	Regions []Region `json:"regions"`
}

// Region is one located region in the model's reply.
type Region struct {
	ID   string    `json:"id"`
	BBox []float64 `json:"bbox"`
	Text string    `json:"text"`
}

// Result is the score of one probe call against the ground truth.
type Result struct {
	Returned   int     `json:"returned"`
	IoUMean    float64 `json:"iouMean"`
	IoUOk      int     `json:"iouOk"`
	TextOk     int     `json:"textOk"`
	PixelSpace int     `json:"pixelSpace"`
}

// IoUPass mirrors the methodology validated on the uoma gateway: a region
// counts as located when IoU >= 0.5.
const IoUPass = 0.5

// Score compares the model's parsed reply against the ground truth. Regions
// that answer in pixel space — a known failure mode when thinking is disabled —
// are re-scaled before scoring and counted in PixelSpace, so a coordinate-
// convention violation is not mistaken for poor localization. IoUMean divides
// by all targets: a region absent from the reply counts as IoU 0.
func Score(gt map[string][4]float64, parsed *Reply) Result {
	var res Result
	if parsed == nil {
		return res
	}
	res.Returned = len(parsed.Regions)
	byID := make(map[string]Region, len(parsed.Regions))
	for _, r := range parsed.Regions {
		byID[r.ID] = r
	}
	for _, t := range Targets {
		r, ok := byID[t.ID]
		if !ok || len(r.BBox) != 4 {
			continue
		}
		var box [4]float64
		copy(box[:], r.BBox)
		if maxOf(box) > 1.5 {
			res.PixelSpace++
			box = [4]float64{box[0] / W, box[1] / H, box[2] / W, box[3] / H}
		}
		// Inverted or zero-size replies score IoU 0, like the retired probe —
		// corner sorting would reward them.
		var v float64
		if box[2] > box[0] && box[3] > box[1] {
			v = geometry.IoU(
				geometry.PixelBox{X0: box[0], Y0: box[1], X1: box[2], Y1: box[3]},
				geometry.PixelBox{X0: gt[t.ID][0], Y0: gt[t.ID][1], X1: gt[t.ID][2], Y1: gt[t.ID][3]},
			)
		}
		res.IoUMean += v
		if v >= IoUPass {
			res.IoUOk++
		}
		if normText(r.Text) == normText(t.Text) {
			res.TextOk++
		}
	}
	res.IoUMean /= float64(len(Targets))
	return res
}

// maxOf returns the largest coordinate, for the pixel-space violation check.
func maxOf(b [4]float64) float64 {
	m := b[0]
	for _, v := range b[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

// normText mirrors the retired probe's comparison: case-insensitive with
// whitespace collapsed to single spaces.
func normText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
