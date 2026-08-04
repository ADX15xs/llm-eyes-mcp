package geometry

import (
	"math"
	"testing"
)

const eps = 1e-9

func almost(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// ---------------------------------------------------------------------------
// Box normalisation - VLMs emit swapped and out-of-range corners routinely
// ---------------------------------------------------------------------------

func TestBoxNormalized(t *testing.T) {
	cases := []struct {
		name string
		in   Box
		want Box
	}{
		{"already valid", Box{X0: 0.1, Y0: 0.2, X1: 0.3, Y1: 0.4}, Box{X0: 0.1, Y0: 0.2, X1: 0.3, Y1: 0.4}},
		{"swapped x", Box{X0: 0.8, Y0: 0.2, X1: 0.3, Y1: 0.4}, Box{X0: 0.3, Y0: 0.2, X1: 0.8, Y1: 0.4}},
		{"swapped y", Box{X0: 0.1, Y0: 0.9, X1: 0.3, Y1: 0.4}, Box{X0: 0.1, Y0: 0.4, X1: 0.3, Y1: 0.9}},
		{"negative clamped", Box{X0: -0.5, Y0: -2, X1: 0.3, Y1: 0.4}, Box{X0: 0, Y0: 0, X1: 0.3, Y1: 0.4}},
		{"over one clamped", Box{X0: 0.1, Y0: 0.2, X1: 1.7, Y1: 3}, Box{X0: 0.1, Y0: 0.2, X1: 1, Y1: 1}},
		{"degenerate point", Box{X0: 0.5, Y0: 0.5, X1: 0.5, Y1: 0.5}, Box{X0: 0.5, Y0: 0.5, X1: 0.5, Y1: 0.5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.Normalized()
			if math.Abs(got.X0-tc.want.X0) > eps || math.Abs(got.Y0-tc.want.Y0) > eps ||
				math.Abs(got.X1-tc.want.X1) > eps || math.Abs(got.Y1-tc.want.Y1) > eps {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestBoxNormalizedNaNBecomesZero(t *testing.T) {
	got := Box{X0: math.NaN(), Y0: 0.1, X1: 0.5, Y1: 0.6}.Normalized()
	if math.IsNaN(got.X0) {
		t.Error("NaN leaked through normalisation - it would poison every downstream number")
	}
}

func TestBoxScale(t *testing.T) {
	b := Box{Label: "logo", X0: 0.1, Y0: 0.2, X1: 0.5, Y1: 0.6}
	p := b.Scale(1000, 500)
	almost(t, "X0", p.X0, 100)
	almost(t, "Y0", p.Y0, 100)
	almost(t, "X1", p.X1, 500)
	almost(t, "Y1", p.Y1, 300)
	almost(t, "Width", p.Width(), 400)
	almost(t, "Height", p.Height(), 200)
	almost(t, "Area", p.Area(), 80000)
	if p.Label != "logo" {
		t.Errorf("label lost during scaling: %q", p.Label)
	}
}

// ---------------------------------------------------------------------------
// distance primitives
// ---------------------------------------------------------------------------

func TestCenterDistanceIsPythagorean(t *testing.T) {
	a := PixelBox{X0: 0, Y0: 0, X1: 0, Y1: 0}     // centre (0,0)
	b := PixelBox{X0: 30, Y0: 40, X1: 30, Y1: 40} // centre (30,40)
	almost(t, "CenterDistance", CenterDistance(a, b), 50)
	// Symmetric.
	almost(t, "reverse", CenterDistance(b, a), 50)
}

func TestEdgeGap(t *testing.T) {
	cases := []struct {
		name string
		a, b PixelBox
		want float64
	}{
		{
			"horizontal gap only",
			PixelBox{X0: 0, Y0: 0, X1: 10, Y1: 10},
			PixelBox{X0: 30, Y0: 0, X1: 40, Y1: 10},
			20,
		},
		{
			"vertical gap only",
			PixelBox{X0: 0, Y0: 0, X1: 10, Y1: 10},
			PixelBox{X0: 0, Y0: 25, X1: 10, Y1: 35},
			15,
		},
		{
			"diagonal gap - both axes separated",
			PixelBox{X0: 0, Y0: 0, X1: 10, Y1: 10},
			PixelBox{X0: 13, Y0: 14, X1: 20, Y1: 20},
			5, // hypot(3,4)
		},
		{
			"touching",
			PixelBox{X0: 0, Y0: 0, X1: 10, Y1: 10},
			PixelBox{X0: 10, Y0: 0, X1: 20, Y1: 10},
			0,
		},
		{
			"overlapping",
			PixelBox{X0: 0, Y0: 0, X1: 10, Y1: 10},
			PixelBox{X0: 5, Y0: 5, X1: 15, Y1: 15},
			0,
		},
		{
			"contained",
			PixelBox{X0: 0, Y0: 0, X1: 100, Y1: 100},
			PixelBox{X0: 40, Y0: 40, X1: 60, Y1: 60},
			0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			almost(t, "EdgeGap", EdgeGap(tc.a, tc.b), tc.want)
			almost(t, "EdgeGap reversed", EdgeGap(tc.b, tc.a), tc.want)
		})
	}
}

func TestIntersectionAndIoU(t *testing.T) {
	a := PixelBox{X0: 0, Y0: 0, X1: 10, Y1: 10} // 100
	b := PixelBox{X0: 5, Y0: 5, X1: 15, Y1: 15} // 100, overlap 25
	almost(t, "IntersectionArea", IntersectionArea(a, b), 25)
	almost(t, "IoU", IoU(a, b), 25.0/175.0)

	disjoint := PixelBox{X0: 100, Y0: 100, X1: 110, Y1: 110}
	almost(t, "disjoint intersection", IntersectionArea(a, disjoint), 0)
	almost(t, "disjoint IoU", IoU(a, disjoint), 0)

	almost(t, "self IoU", IoU(a, a), 1)

	degenerate := PixelBox{}
	almost(t, "degenerate IoU", IoU(degenerate, degenerate), 0) // no divide-by-zero
}

// ---------------------------------------------------------------------------
// Distance
// ---------------------------------------------------------------------------

func TestDistanceResult(t *testing.T) {
	a := PixelBox{Label: "logo", X0: 0, Y0: 0, X1: 20, Y1: 20}    // centre (10,10)
	b := PixelBox{Label: "title", X0: 40, Y0: 50, X1: 60, Y1: 70} // centre (50,60)
	r := Distance(a, b)

	almost(t, "Value", r.Value, 64.03) // hypot(40,50)=64.0312 -> 64.03
	if r.Unit != "pixels" {
		t.Errorf("Unit = %q", r.Unit)
	}
	if r.Details["metric"] != "center_distance" {
		t.Errorf("metric = %v", r.Details["metric"])
	}
	almost(t, "dx", r.Details["dx"].(float64), 40)
	almost(t, "dy", r.Details["dy"].(float64), 50)
	almost(t, "edge_gap", r.Details["edge_gap"].(float64), 36.06) // hypot(20,30)
	if r.Details["overlapping"] != false {
		t.Errorf("overlapping = %v, want false", r.Details["overlapping"])
	}
	if r.Summary == "" {
		t.Error("summary must never be empty: it is what the agent reads")
	}
	boxes, ok := r.Details["boxes"].([]map[string]any)
	if !ok || len(boxes) != 2 {
		t.Fatalf("boxes detail = %#v", r.Details["boxes"])
	}
}

func TestDistanceReportsOverlap(t *testing.T) {
	a := PixelBox{Label: "a", X0: 0, Y0: 0, X1: 100, Y1: 100}
	b := PixelBox{Label: "b", X0: 50, Y0: 50, X1: 150, Y1: 150}
	r := Distance(a, b)
	if r.Details["overlapping"] != true {
		t.Error("overlapping boxes must be flagged")
	}
	almost(t, "edge_gap", r.Details["edge_gap"].(float64), 0)
}

// ---------------------------------------------------------------------------
// Area
// ---------------------------------------------------------------------------

func TestAreaSingleBox(t *testing.T) {
	b := PixelBox{Label: "button", X0: 10, Y0: 10, X1: 110, Y1: 60} // 100x50 = 5000
	r := Area([]PixelBox{b}, 1000, 500)                             // canvas 500000

	almost(t, "Value", r.Value, 5000)
	if r.Unit != "square_pixels" {
		t.Errorf("Unit = %q", r.Unit)
	}
	items := r.Details["boxes"].([]map[string]any)
	almost(t, "percent_of_image", items[0]["percent_of_image"].(float64), 1) // 5000/500000 = 1%
	almost(t, "width", items[0]["width"].(float64), 100)
	almost(t, "height", items[0]["height"].(float64), 50)
}

func TestAreaTwoBoxesReportsIntersection(t *testing.T) {
	a := PixelBox{Label: "a", X0: 0, Y0: 0, X1: 10, Y1: 10}
	b := PixelBox{Label: "b", X0: 5, Y0: 5, X1: 15, Y1: 15}
	r := Area([]PixelBox{a, b}, 100, 100)

	almost(t, "total_area", r.Details["total_area"].(float64), 200)
	almost(t, "intersection_area", r.Details["intersection_area"].(float64), 25)
	almost(t, "combined_area", r.Details["combined_area"].(float64), 175) // union
	if _, ok := r.Details["iou"]; !ok {
		t.Error("iou missing for the two-box case")
	}
}

func TestAreaZeroCanvasDoesNotDivideByZero(t *testing.T) {
	b := PixelBox{Label: "x", X0: 0, Y0: 0, X1: 10, Y1: 10}
	r := Area([]PixelBox{b}, 0, 0)
	if math.IsNaN(r.Value) || math.IsInf(r.Value, 0) {
		t.Errorf("Value = %v", r.Value)
	}
	items := r.Details["boxes"].([]map[string]any)
	if _, ok := items[0]["percent_of_image"]; ok {
		t.Error("percent_of_image should be omitted when the canvas is unknown")
	}
}

// ---------------------------------------------------------------------------
// Alignment
// ---------------------------------------------------------------------------

func TestAlignmentSingleBoxPerfectlyCentered(t *testing.T) {
	// Image 1000x500, centre (500,250). Box centred exactly.
	b := PixelBox{Label: "logo", X0: 450, Y0: 200, X1: 550, Y1: 300}
	r := Alignment([]PixelBox{b}, 1000, 500)

	almost(t, "Value", r.Value, 0)
	almost(t, "offset_x", r.Details["offset_x"].(float64), 0)
	almost(t, "offset_y", r.Details["offset_y"].(float64), 0)
	if r.Details["horizontally_center"] != true || r.Details["vertically_center"] != true {
		t.Errorf("should be centred on both axes: %#v", r.Details)
	}
	if r.Details["out_of_bounds"] != false {
		t.Error("box is inside the canvas but was flagged out of bounds")
	}
}

func TestAlignmentSingleBoxWithinTolerance(t *testing.T) {
	// Tolerance is 2% => 20 px horizontally on a 1000 px wide image.
	inside := PixelBox{Label: "a", X0: 465, Y0: 200, X1: 565, Y1: 300} // centre x=515, off by 15
	r := Alignment([]PixelBox{inside}, 1000, 500)
	if r.Details["horizontally_center"] != true {
		t.Errorf("15 px off on a 1000 px image is inside the 20 px tolerance: %#v", r.Details)
	}

	outside := PixelBox{Label: "a", X0: 480, Y0: 200, X1: 580, Y1: 300} // centre x=530, off by 30
	r2 := Alignment([]PixelBox{outside}, 1000, 500)
	if r2.Details["horizontally_center"] != false {
		t.Errorf("30 px off exceeds the 20 px tolerance: %#v", r2.Details)
	}
	almost(t, "offset_x_percent", r2.Details["offset_x_percent"].(float64), 3)
}

func TestAlignmentDetectsOutOfBounds(t *testing.T) {
	b := PixelBox{Label: "overflow", X0: -40, Y0: 10, X1: 60, Y1: 110}
	r := Alignment([]PixelBox{b}, 1000, 500)
	if r.Details["out_of_bounds"] != true {
		t.Error("a box extending past the left edge must be flagged")
	}
}

func TestAlignmentTwoBoxesLeftAligned(t *testing.T) {
	// Same X0, different widths and rows => "left" aligned, "right" not.
	a := PixelBox{Label: "a", X0: 100, Y0: 0, X1: 200, Y1: 50}
	b := PixelBox{Label: "b", X0: 100, Y0: 80, X1: 400, Y1: 130}
	r := Alignment([]PixelBox{a, b}, 1000, 500)

	axes := r.Details["axes"].(map[string]any)
	left := axes["left"].(map[string]any)
	if left["aligned"] != true {
		t.Errorf("left should be aligned: %#v", left)
	}
	right := axes["right"].(map[string]any)
	if right["aligned"] != false {
		t.Errorf("right should NOT be aligned (200 vs 400): %#v", right)
	}
	if r.Details["tightest_axis"] != "left" {
		t.Errorf("tightest_axis = %v, want left", r.Details["tightest_axis"])
	}
	almost(t, "Value", r.Value, 0)

	alignedAxes := r.Details["aligned_axes"].([]string)
	found := false
	for _, ax := range alignedAxes {
		if ax == "left" {
			found = true
		}
	}
	if !found {
		t.Errorf("aligned_axes = %v, want it to contain left", alignedAxes)
	}
}

func TestAlignmentNoAxisAligned(t *testing.T) {
	a := PixelBox{Label: "a", X0: 0, Y0: 0, X1: 50, Y1: 50}
	b := PixelBox{Label: "b", X0: 400, Y0: 300, X1: 600, Y1: 480}
	r := Alignment([]PixelBox{a, b}, 1000, 500)

	if axes := r.Details["aligned_axes"]; axes != nil {
		if s, ok := axes.([]string); ok && len(s) > 0 {
			t.Errorf("aligned_axes = %v, want empty", s)
		}
	}
	if r.Summary == "" {
		t.Error("summary missing")
	}
}

func TestAlignmentThreeBoxes(t *testing.T) {
	// Three items in a row, tops equal => top aligned.
	boxes := []PixelBox{
		{Label: "a", X0: 0, Y0: 100, X1: 50, Y1: 150},
		{Label: "b", X0: 100, Y0: 100, X1: 150, Y1: 160},
		{Label: "c", X0: 200, Y0: 100, X1: 250, Y1: 140},
	}
	r := Alignment(boxes, 1000, 500)
	if r.Details["box_count"] != 3 {
		t.Errorf("box_count = %v", r.Details["box_count"])
	}
	axes := r.Details["axes"].(map[string]any)
	if axes["top"].(map[string]any)["aligned"] != true {
		t.Errorf("top should be aligned: %#v", axes["top"])
	}
	if axes["bottom"].(map[string]any)["aligned"] != false {
		t.Errorf("bottom should not be aligned: %#v", axes["bottom"])
	}
	// 3 boxes => 3 pairs.
	if pairs := r.Details["pairwise_iou"].([]map[string]any); len(pairs) != 3 {
		t.Errorf("pairwise_iou has %d entries, want 3", len(pairs))
	}
}

// ---------------------------------------------------------------------------
// Round2
// ---------------------------------------------------------------------------

func TestRound2(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{1.234, 1.23},
		{1.235, 1.24},
		{1.2349, 1.23},
		{-1.235, -1.24},
		{0, 0},
		{100, 100},
	}
	for _, tc := range cases {
		if got := Round2(tc.in); math.Abs(got-tc.want) > eps {
			t.Errorf("Round2(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSpreadOfEmpty(t *testing.T) {
	if got := spreadOf(nil); got != 0 {
		t.Errorf("spreadOf(nil) = %v, want 0", got)
	}
}
