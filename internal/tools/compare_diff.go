package tools

import (
	"bytes"
	"fmt"
	"image"
	"strings"

	"github.com/xiaos/llm-eyes-mcp/internal/geometry"
	"github.com/xiaos/llm-eyes-mcp/internal/imageio"
	"github.com/xiaos/llm-eyes-mcp/internal/imgproc"
)

type regionDiff struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	Found      bool      `json:"found"`
	Confidence float64   `json:"confidence,omitempty"`
	BBox       []float64 `json:"bbox,omitempty"` // located, original px
	SpecBBox   []float64 `json:"spec_bbox,omitempty"`
	IOU        float64   `json:"iou,omitempty"`
	CenterDist float64   `json:"center_dist_px,omitempty"`
	Issues     []string  `json:"issues,omitempty"`
	// Color
	ExpectedColor string  `json:"expected_color,omitempty"`
	ActualColor   string  `json:"actual_color,omitempty"`
	ColorDelta    float64 `json:"color_delta,omitempty"`
	// Text
	ExpectedText string `json:"expected_text,omitempty"`
	ActualText   string `json:"actual_text,omitempty"`
}

type compareDiff struct {
	regions []regionDiff
	ok      int
}

func (t *CompareTool) diffRegions(spec *compareSpec, img *imageio.Image, dets []regionDetection) compareDiff {
	var diff compareDiff
	src, _, decodeErr := image.Decode(bytes.NewReader(img.Data))
	if decodeErr != nil {
		t.d.logf("warning: could not decode original image for color sampling: %v", decodeErr)
	}

	scaleX := 1.0
	scaleY := 1.0
	if spec.Image != nil && spec.Image.Width > 0 && spec.Image.Height > 0 {
		scaleX = float64(img.Width) / float64(spec.Image.Width)
		scaleY = float64(img.Height) / float64(spec.Image.Height)
	}

	foundMap := map[string]regionDetection{}
	for _, d := range dets {
		foundMap[d.ID] = d
	}

	for _, r := range spec.Regions {
		diff.regions = append(diff.regions, t.diffOne(r, foundMap, img, src, scaleX, scaleY))
	}
	diff.ok = 0
	for _, rd := range diff.regions {
		if rd.Status == "ok" {
			diff.ok++
		}
	}
	return diff
}

func (t *CompareTool) diffOne(r compareRegion, found map[string]regionDetection, img *imageio.Image, src image.Image, sx, sy float64) regionDiff {
	rd := regionDiff{ID: r.ID}
	det, has := found[r.ID]
	rd.Found = has && det.Found
	rd.Confidence = det.Confidence
	if has && det.Found {
		box := det.Box.Scale(img.Width, img.Height)
		rd.BBox = []float64{box.X0, box.Y0, box.X1, box.Y1}
		rd.ActualText = det.Text
	}

	if r.BBox != nil && len(r.BBox) == 4 {
		sb := specBoxToPixel(r.BBox, sx, sy)
		rd.SpecBBox = []float64{sb[0], sb[1], sb[2], sb[3]}
		if has && det.Found {
			ib := boxToPixel(det.Box, img.Width, img.Height)
			specBox := geometry.PixelBox{X0: sb[0], Y0: sb[1], X1: sb[2], Y1: sb[3]}
			imgBox := geometry.PixelBox{X0: ib[0], Y0: ib[1], X1: ib[2], Y1: ib[3]}
			iou := geometry.IoU(specBox, imgBox)
			rd.IOU = iou
			cd := geometry.CenterDistance(specBox, imgBox)
			rd.CenterDist = cd
			if iou < 0.5 {
				rd.Status = "offset"
				rd.Issues = append(rd.Issues, fmt.Sprintf("offset(iou=%.2f,center=%.1fpx)", iou, cd))
			}
		}
	}

	if r.Color != "" {
		rd.ExpectedColor = r.Color
		if has && det.Found {
			ib := boxToPixel(det.Box, img.Width, img.Height)
			sampled := false
			if src != nil {
				rect := image.Rectangle{
					Min: image.Point{X: int(ib[0]), Y: int(ib[1])},
					Max: image.Point{X: int(ib[2]), Y: int(ib[3])},
				}
				if !rect.Empty() && !rect.Intersect(src.Bounds()).Empty() {
					sample := imgproc.AverageRGB(src, rect, 0.2)
					rd.ActualColor = sample.Hex()
					rd.ColorDelta = sample.Delta(hexToRGB(r.Color))
					sampled = true
				}
			}
			switch {
			case !sampled:
				if rd.Status == "" {
					rd.Status = "color_unverified"
				} else {
					rd.Status += "+color_unverified"
				}
				rd.Issues = append(rd.Issues, "color_unverified: could not sample pixels")
			case rd.ColorDelta > 24:
				if rd.Status == "" {
					rd.Status = "color_mismatch"
				} else {
					rd.Status += "+color_mismatch"
				}
				rd.Issues = append(rd.Issues, fmt.Sprintf("color_delta=%.1f(expected=%s actual=%s)", rd.ColorDelta, r.Color, rd.ActualColor))
			}
		}
	}

	if r.Text != "" {
		rd.ExpectedText = normalizeWhitespace(r.Text)
		if has && det.Found {
			got := normalizeWhitespace(det.Text)
			rd.ActualText = got
			if got != rd.ExpectedText {
				if rd.Status == "" {
					rd.Status = "text_mismatch"
				} else {
					rd.Status += "+text_mismatch"
				}
				rd.Issues = append(rd.Issues, fmt.Sprintf("text(expected=%q got=%q)", r.Text, det.Text))
			}
		}
	}

	if rd.Status == "" {
		if rd.Found {
			rd.Status = "ok"
		} else {
			rd.Status = "missing"
			rd.Issues = append(rd.Issues, "not found in screenshot")
		}
	}
	return rd
}

func specBoxToPixel(bbox []float64, sx, sy float64) [4]float64 {
	return [4]float64{
		bbox[0] * sx,
		bbox[1] * sy,
		bbox[2] * sx,
		bbox[3] * sy,
	}
}

func boxToPixel(b geometry.Box, pw, ph int) [4]float64 {
	return [4]float64{
		b.X0 * float64(pw),
		b.Y0 * float64(ph),
		b.X1 * float64(pw),
		b.Y1 * float64(ph),
	}
}

func normalizeWhitespace(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	var lastSpace bool
	for _, r := range s {
		switch {
		case r == '\t', r == '\n', r == '\r', r == ' ', r == '\u00A0', r == '\u3000':
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			b.WriteRune(r)
			lastSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

func hexToRGB(h string) imgproc.RGB {
	var r, g, b int
	fmt.Sscanf(h, "#%02x%02x%02x", &r, &g, &b)
	return imgproc.RGB{R: float64(r), G: float64(g), B: float64(b)}
}
