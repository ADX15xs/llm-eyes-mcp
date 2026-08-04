// Package geometry performs the deterministic arithmetic that the VLM must not
// be trusted to do. The VLM only supplies normalised bounding boxes; every
// number returned to the agent is computed here with the standard math package.
package geometry

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Box is a normalised bounding box in [0,1] image coordinates, origin top-left.
type Box struct {
	Label string  `json:"label"`
	X0    float64 `json:"x0"`
	Y0    float64 `json:"y0"`
	X1    float64 `json:"x1"`
	Y1    float64 `json:"y1"`
	// Confidence is optional; VLMs that omit it leave this at 0.
	Confidence float64 `json:"confidence,omitempty"`
}

// Normalized clamps the box to [0,1] and orders its corners. VLMs routinely
// emit swapped or slightly out-of-range corners.
func (b Box) Normalized() Box {
	out := b
	if out.X0 > out.X1 {
		out.X0, out.X1 = out.X1, out.X0
	}
	if out.Y0 > out.Y1 {
		out.Y0, out.Y1 = out.Y1, out.Y0
	}
	out.X0 = clamp01(out.X0)
	out.Y0 = clamp01(out.Y0)
	out.X1 = clamp01(out.X1)
	out.Y1 = clamp01(out.Y1)
	return out
}

// Scale converts a normalised box into pixel space.
func (b Box) Scale(width, height int) PixelBox {
	n := b.Normalized()
	w, h := float64(width), float64(height)
	return PixelBox{
		Label: n.Label,
		X0:    n.X0 * w,
		Y0:    n.Y0 * h,
		X1:    n.X1 * w,
		Y1:    n.Y1 * h,
	}
}

// PixelBox is a bounding box in absolute pixel coordinates.
type PixelBox struct {
	Label string  `json:"label"`
	X0    float64 `json:"x0"`
	Y0    float64 `json:"y0"`
	X1    float64 `json:"x1"`
	Y1    float64 `json:"y1"`
}

// Width returns the box width in pixels.
func (p PixelBox) Width() float64 { return p.X1 - p.X0 }

// Height returns the box height in pixels.
func (p PixelBox) Height() float64 { return p.Y1 - p.Y0 }

// Area returns the box area in square pixels.
func (p PixelBox) Area() float64 { return p.Width() * p.Height() }

// CenterX returns the horizontal centre in pixels.
func (p PixelBox) CenterX() float64 { return (p.X0 + p.X1) / 2 }

// CenterY returns the vertical centre in pixels.
func (p PixelBox) CenterY() float64 { return (p.Y0 + p.Y1) / 2 }

// CenterDistance is the Euclidean distance between two box centres.
func CenterDistance(a, b PixelBox) float64 {
	return math.Hypot(b.CenterX()-a.CenterX(), b.CenterY()-a.CenterY())
}

// EdgeGap is the shortest distance between the two rectangles' borders. It is
// zero when the boxes touch or overlap. Axes that already overlap contribute 0,
// which is what makes this the true nearest-edge distance rather than a naive
// corner-to-corner measure.
func EdgeGap(a, b PixelBox) float64 {
	dx := math.Max(0, math.Max(a.X0, b.X0)-math.Min(a.X1, b.X1))
	dy := math.Max(0, math.Max(a.Y0, b.Y0)-math.Min(a.Y1, b.Y1))
	return math.Hypot(dx, dy)
}

// IntersectionArea returns the overlapping area of two boxes in square pixels.
func IntersectionArea(a, b PixelBox) float64 {
	w := math.Max(0, math.Min(a.X1, b.X1)-math.Max(a.X0, b.X0))
	h := math.Max(0, math.Min(a.Y1, b.Y1)-math.Max(a.Y0, b.Y0))
	return w * h
}

// IoU is intersection over union. Returns 0 when both boxes are degenerate.
func IoU(a, b PixelBox) float64 {
	inter := IntersectionArea(a, b)
	union := a.Area() + b.Area() - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

// Result is the deterministic output of one measurement.
type Result struct {
	// Value is the headline number the agent asked for.
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
	// Summary is a one-line human/agent readable statement.
	Summary string `json:"summary"`
	// Details carries the supporting numbers so the LLM can reason further
	// without a second round trip.
	Details map[string]any `json:"details"`
}

// Round2 rounds to two decimals. Reporting more precision than the VLM's
// coordinate quality supports would be false confidence.
func Round2(v float64) float64 { return math.Round(v*100) / 100 }

// Distance measures between exactly two boxes.
func Distance(a, b PixelBox) Result {
	dx := b.CenterX() - a.CenterX()
	dy := b.CenterY() - a.CenterY()
	center := math.Hypot(dx, dy)
	gap := EdgeGap(a, b)
	overlap := IntersectionArea(a, b) > 0

	return Result{
		Value:   Round2(center),
		Unit:    "pixels",
		Summary: fmt.Sprintf("Center-to-center distance between %q and %q is %.2f px (dx=%.2f, dy=%.2f); nearest-edge gap is %.2f px.", a.Label, b.Label, center, dx, dy, gap),
		Details: map[string]any{
			"metric":          "center_distance",
			"center_distance": Round2(center),
			"edge_gap":        Round2(gap),
			"dx":              Round2(dx),
			"dy":              Round2(dy),
			"overlapping":     overlap,
			"iou":             Round2(IoU(a, b)),
			"boxes":           boxSummaries(a, b),
		},
	}
}

// Area measures one or more boxes against the image canvas.
func Area(boxes []PixelBox, imgW, imgH int) Result {
	canvas := float64(imgW) * float64(imgH)
	items := make([]map[string]any, 0, len(boxes))
	total := 0.0
	for _, b := range boxes {
		a := b.Area()
		total += a
		entry := map[string]any{
			"label":  b.Label,
			"area":   Round2(a),
			"width":  Round2(b.Width()),
			"height": Round2(b.Height()),
		}
		if canvas > 0 {
			entry["percent_of_image"] = Round2(a / canvas * 100)
		}
		items = append(items, entry)
	}

	details := map[string]any{
		"metric":     "area",
		"boxes":      items,
		"canvas_px2": Round2(canvas),
	}
	var summary string
	if len(boxes) == 1 {
		b := boxes[0]
		pct := 0.0
		if canvas > 0 {
			pct = b.Area() / canvas * 100
		}
		summary = fmt.Sprintf("%q covers %.2f px² (%.2f%% of the %dx%d image), bounding box %.2fx%.2f px.",
			b.Label, b.Area(), pct, imgW, imgH, b.Width(), b.Height())
	} else {
		inter := IntersectionArea(boxes[0], boxes[1])
		details["intersection_area"] = Round2(inter)
		details["iou"] = Round2(IoU(boxes[0], boxes[1]))
		details["combined_area"] = Round2(total - inter)
		summary = fmt.Sprintf("Areas: %s. Intersection %.2f px², IoU %.4f.",
			joinAreas(boxes), inter, IoU(boxes[0], boxes[1]))
	}
	details["total_area"] = Round2(total)

	value := total
	if len(boxes) == 1 {
		value = boxes[0].Area()
	}
	return Result{Value: Round2(value), Unit: "square_pixels", Summary: summary, Details: details}
}

// CenterToleranceRatio is the fraction of the image dimension within which an
// element counts as centred (2% of width/height).
const CenterToleranceRatio = 0.02

// AlignToleranceRatio is the fraction of the image dimension within which two
// edges count as aligned (1% of width/height).
const AlignToleranceRatio = 0.01

// Alignment checks centring (single box) or mutual alignment (two or more).
func Alignment(boxes []PixelBox, imgW, imgH int) Result {
	w, h := float64(imgW), float64(imgH)

	if len(boxes) == 1 {
		b := boxes[0]
		offX := b.CenterX() - w/2
		offY := b.CenterY() - h/2
		tolX, tolY := w*CenterToleranceRatio, h*CenterToleranceRatio
		centeredX := math.Abs(offX) <= tolX
		centeredY := math.Abs(offY) <= tolY
		outOfBounds := b.X0 < -0.5 || b.Y0 < -0.5 || b.X1 > w+0.5 || b.Y1 > h+0.5
		worst := math.Max(math.Abs(offX), math.Abs(offY))

		return Result{
			Value: Round2(worst),
			Unit:  "pixels",
			Summary: fmt.Sprintf("%q centre is offset (%.2f, %.2f) px from the image centre; horizontally centred=%v, vertically centred=%v (tolerance %.1f/%.1f px).",
				b.Label, offX, offY, centeredX, centeredY, tolX, tolY),
			Details: map[string]any{
				"metric":              "center_offset",
				"offset_x":            Round2(offX),
				"offset_y":            Round2(offY),
				"offset_x_percent":    percent(offX, w),
				"offset_y_percent":    percent(offY, h),
				"horizontally_center": centeredX,
				"vertically_center":   centeredY,
				"tolerance_px":        map[string]any{"x": Round2(tolX), "y": Round2(tolY)},
				"out_of_bounds":       outOfBounds,
				"image":               map[string]any{"width": imgW, "height": imgH},
				"boxes":               boxSummaries(boxes...),
			},
		}
	}

	// Two or more boxes: measure spread across each alignment axis.
	axes := []struct {
		name string
		vals []float64
		tol  float64
	}{
		{"left", pluck(boxes, func(b PixelBox) float64 { return b.X0 }), w * AlignToleranceRatio},
		{"center_x", pluck(boxes, PixelBox.CenterX), w * AlignToleranceRatio},
		{"right", pluck(boxes, func(b PixelBox) float64 { return b.X1 }), w * AlignToleranceRatio},
		{"top", pluck(boxes, func(b PixelBox) float64 { return b.Y0 }), h * AlignToleranceRatio},
		{"center_y", pluck(boxes, PixelBox.CenterY), h * AlignToleranceRatio},
		{"bottom", pluck(boxes, func(b PixelBox) float64 { return b.Y1 }), h * AlignToleranceRatio},
	}

	axisDetail := make(map[string]any, len(axes))
	bestAxis, bestSpread := "", math.Inf(1)
	var aligned []string
	for _, ax := range axes {
		spread := spreadOf(ax.vals)
		ok := spread <= ax.tol
		axisDetail[ax.name] = map[string]any{
			"max_deviation_px": Round2(spread),
			"tolerance_px":     Round2(ax.tol),
			"aligned":          ok,
		}
		if ok {
			aligned = append(aligned, ax.name)
		}
		if spread < bestSpread {
			bestSpread, bestAxis = spread, ax.name
		}
	}
	sort.Strings(aligned)

	labels := make([]string, 0, len(boxes))
	for _, b := range boxes {
		labels = append(labels, fmt.Sprintf("%q", b.Label))
	}
	verdict := "no axis is aligned within tolerance"
	if len(aligned) > 0 {
		verdict = "aligned on: " + strings.Join(aligned, ", ")
	}

	return Result{
		Value: Round2(bestSpread),
		Unit:  "pixels",
		Summary: fmt.Sprintf("Alignment of %s: %s. Tightest axis is %s with %.2f px maximum deviation.",
			strings.Join(labels, ", "), verdict, bestAxis, bestSpread),
		Details: map[string]any{
			"metric":         "alignment_deviation",
			"tightest_axis":  bestAxis,
			"aligned_axes":   aligned,
			"axes":           axisDetail,
			"pairwise_iou":   pairwiseIoU(boxes),
			"image":          map[string]any{"width": imgW, "height": imgH},
			"boxes":          boxSummaries(boxes...),
			"box_count":      len(boxes),
		},
	}
}

func pluck(boxes []PixelBox, f func(PixelBox) float64) []float64 {
	out := make([]float64, len(boxes))
	for i, b := range boxes {
		out[i] = f(b)
	}
	return out
}

func spreadOf(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	lo, hi := vals[0], vals[0]
	for _, v := range vals[1:] {
		lo = math.Min(lo, v)
		hi = math.Max(hi, v)
	}
	return hi - lo
}

func pairwiseIoU(boxes []PixelBox) []map[string]any {
	var out []map[string]any
	for i := 0; i < len(boxes); i++ {
		for j := i + 1; j < len(boxes); j++ {
			out = append(out, map[string]any{
				"a":   boxes[i].Label,
				"b":   boxes[j].Label,
				"iou": Round2(IoU(boxes[i], boxes[j])),
			})
		}
	}
	return out
}

func boxSummaries(boxes ...PixelBox) []map[string]any {
	out := make([]map[string]any, 0, len(boxes))
	for _, b := range boxes {
		out = append(out, map[string]any{
			"label":    b.Label,
			"x0":       Round2(b.X0),
			"y0":       Round2(b.Y0),
			"x1":       Round2(b.X1),
			"y1":       Round2(b.Y1),
			"center_x": Round2(b.CenterX()),
			"center_y": Round2(b.CenterY()),
		})
	}
	return out
}

func joinAreas(boxes []PixelBox) string {
	parts := make([]string, 0, len(boxes))
	for _, b := range boxes {
		parts = append(parts, fmt.Sprintf("%q=%.2f px²", b.Label, b.Area()))
	}
	return strings.Join(parts, ", ")
}

func percent(v, total float64) float64 {
	if total == 0 {
		return 0
	}
	return Round2(v / total * 100)
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(0, math.Min(1, v))
}
