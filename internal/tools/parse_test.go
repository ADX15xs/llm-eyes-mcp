package tools

import (
	"math"
	"testing"

	"github.com/xiaos/llm-eyes-mcp/internal/geometry"
)

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// ---------------------------------------------------------------------------
// extractJSON - models wrap JSON in fences and prose no matter what you ask
// ---------------------------------------------------------------------------

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare object", `{"a":1}`, `{"a":1}`},
		{"bare array", `[{"a":1}]`, `[{"a":1}]`},
		{"padded whitespace", "\n  {\"a\":1}  \n", `{"a":1}`},
		{"json fence", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"uppercase fence", "```JSON\n{\"a\":1}\n```", `{"a":1}`},
		{"anonymous fence", "```\n{\"a\":1}\n```", `{"a":1}`},
		{"prose before", `Here is the result: {"a":1}`, `{"a":1}`},
		{"prose both sides", `Sure! {"a":1} Hope that helps.`, `{"a":1}`},
		{"array wins when it comes first", `text [1,2] and {"a":1}`, `[1,2]`},
		{"empty", "", ""},
		{"whitespace only", "   \n ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractJSON(tc.in); got != tc.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExtractJSONMultilineFenceWithProse(t *testing.T) {
	reply := "I analysed the image.\n\n```json\n{\n  \"objects\": [\n    {\"label\": \"logo\", \"bbox\": [0.1, 0.1, 0.2, 0.2]}\n  ]\n}\n```\n\nLet me know if you need more."
	got := extractJSON(reply)
	if got[0] != '{' || got[len(got)-1] != '}' {
		t.Fatalf("extractJSON did not isolate the document: %q", got)
	}
	dets, err := parseDetections(reply, 1000, 1000)
	if err != nil {
		t.Fatalf("parseDetections: %v", err)
	}
	if len(dets) != 1 || dets[0].Label != "logo" {
		t.Errorf("dets = %#v", dets)
	}
}

// ---------------------------------------------------------------------------
// parseDetections - envelope shapes
// ---------------------------------------------------------------------------

func TestParseDetectionsEnvelopeShapes(t *testing.T) {
	box := `"bbox":[0.1,0.2,0.3,0.4]`
	cases := []struct {
		name  string
		reply string
	}{
		{"objects key", `{"objects":[{"label":"logo",` + box + `}]}`},
		{"detections key", `{"detections":[{"label":"logo",` + box + `}]}`},
		{"results key", `{"results":[{"label":"logo",` + box + `}]}`},
		{"items key", `{"items":[{"label":"logo",` + box + `}]}`},
		{"top-level array", `[{"label":"logo",` + box + `}]`},
		{"single bare object", `{"label":"logo",` + box + `}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dets, err := parseDetections(tc.reply, 1000, 1000)
			if err != nil {
				t.Fatalf("parseDetections: %v", err)
			}
			if len(dets) != 1 {
				t.Fatalf("got %d detections, want 1", len(dets))
			}
			d := dets[0]
			if d.Label != "logo" {
				t.Errorf("label = %q", d.Label)
			}
			approx(t, "X0", d.Box.X0, 0.1)
			approx(t, "Y1", d.Box.Y1, 0.4)
			if !d.Found {
				t.Error("Found = false for a valid box")
			}
		})
	}
}

func TestParseDetectionsLabelAliases(t *testing.T) {
	cases := []struct{ name, reply string }{
		{"label", `[{"label":"logo","bbox":[0.1,0.1,0.2,0.2]}]`},
		{"name", `[{"name":"logo","bbox":[0.1,0.1,0.2,0.2]}]`},
		{"object", `[{"object":"logo","bbox":[0.1,0.1,0.2,0.2]}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dets, err := parseDetections(tc.reply, 100, 100)
			if err != nil {
				t.Fatalf("parseDetections: %v", err)
			}
			if dets[0].Label != "logo" {
				t.Errorf("label = %q", dets[0].Label)
			}
		})
	}
}

func TestParseDetectionsBoxFieldAliases(t *testing.T) {
	cases := []struct{ name, reply string }{
		{"bbox", `[{"label":"a","bbox":[0.1,0.2,0.3,0.4]}]`},
		{"box", `[{"label":"a","box":[0.1,0.2,0.3,0.4]}]`},
		{"bounding_box", `[{"label":"a","bounding_box":[0.1,0.2,0.3,0.4]}]`},
		{"bbox_2d (Qwen)", `[{"label":"a","bbox_2d":[0.1,0.2,0.3,0.4]}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dets, err := parseDetections(tc.reply, 100, 100)
			if err != nil {
				t.Fatalf("parseDetections: %v", err)
			}
			approx(t, "X0", dets[0].Box.X0, 0.1)
			approx(t, "Y1", dets[0].Box.Y1, 0.4)
		})
	}
}

func TestParseDetectionsBoxObjectShapes(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  [4]float64
	}{
		{"x0/y0/x1/y1", `[{"label":"a","bbox":{"x0":0.1,"y0":0.2,"x1":0.3,"y1":0.4}}]`, [4]float64{0.1, 0.2, 0.3, 0.4}},
		{"left/top/right/bottom", `[{"label":"a","bbox":{"left":0.1,"top":0.2,"right":0.3,"bottom":0.4}}]`, [4]float64{0.1, 0.2, 0.3, 0.4}},
		{"x/y/width/height", `[{"label":"a","bbox":{"x":0.1,"y":0.2,"width":0.2,"height":0.2}}]`, [4]float64{0.1, 0.2, 0.3, 0.4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dets, err := parseDetections(tc.reply, 100, 100)
			if err != nil {
				t.Fatalf("parseDetections: %v", err)
			}
			b := dets[0].Box
			approx(t, "x0", b.X0, tc.want[0])
			approx(t, "y0", b.Y0, tc.want[1])
			approx(t, "x1", b.X1, tc.want[2])
			approx(t, "y1", b.Y1, tc.want[3])
		})
	}
}

func TestParseDetectionsConfidenceAliases(t *testing.T) {
	dets, err := parseDetections(`[{"label":"a","bbox":[0.1,0.1,0.2,0.2],"confidence":0.9}]`, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	approx(t, "confidence", dets[0].Confidence, 0.9)

	dets, err = parseDetections(`[{"label":"a","bbox":[0.1,0.1,0.2,0.2],"score":0.77}]`, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	approx(t, "score->confidence", dets[0].Confidence, 0.77)

	dets, err = parseDetections(`[{"label":"a","bbox":[0.1,0.1,0.2,0.2]}]`, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	approx(t, "missing confidence", dets[0].Confidence, 0)
}

// An all-zero box with zero confidence is the prompt's "object absent" signal.
func TestParseDetectionsAbsentObjectIsNotFound(t *testing.T) {
	dets, err := parseDetections(`[{"label":"ghost","bbox":[0,0,0,0],"confidence":0}]`, 100, 100)
	if err != nil {
		t.Fatalf("an absent object is a valid answer, not a parse failure: %v", err)
	}
	if dets[0].Found {
		t.Error("Found = true for an all-zero box")
	}
}

func TestParseDetectionsErrors(t *testing.T) {
	cases := []struct{ name, reply string }{
		{"empty reply", ""},
		{"prose only", "I am unable to analyse images."},
		{"empty array", `[]`},
		{"empty envelope", `{"objects":[]}`},
		{"no label", `[{"bbox":[0.1,0.1,0.2,0.2]}]`},
		{"no bbox", `[{"label":"a"}]`},
		{"bbox too short", `[{"label":"a","bbox":[0.1,0.2]}]`},
		{"unsupported bbox shape", `[{"label":"a","bbox":{"foo":1}}]`},
		{"broken json", `{"objects":[{"label":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseDetections(tc.reply, 100, 100); err == nil {
				t.Errorf("want an error for %q", tc.reply)
			}
		})
	}
}

func TestParseDetectionsSkipsBadEntriesButKeepsGoodOnes(t *testing.T) {
	reply := `{"objects":[
		{"label":"good","bbox":[0.1,0.1,0.2,0.2]},
		{"bbox":[0.3,0.3,0.4,0.4]},
		{"label":"broken","bbox":"nonsense"},
		{"label":"also_good","bbox":[0.5,0.5,0.6,0.6]}
	]}`
	dets, err := parseDetections(reply, 100, 100)
	if err != nil {
		t.Fatalf("one bad entry must not sink the whole reply: %v", err)
	}
	if len(dets) != 2 {
		t.Fatalf("got %d detections, want 2: %#v", len(dets), dets)
	}
	if dets[0].Label != "good" || dets[1].Label != "also_good" {
		t.Errorf("labels = %q, %q", dets[0].Label, dets[1].Label)
	}
}

// ---------------------------------------------------------------------------
// normaliseCoords - the three coordinate conventions seen in the wild
// ---------------------------------------------------------------------------

func TestNormaliseCoords(t *testing.T) {
	cases := []struct {
		name       string
		in         [4]float64
		w, h       int
		want       [4]float64
		wantErr    bool
		explanatio string
	}{
		{
			name: "already normalised", in: [4]float64{0.1, 0.2, 0.3, 0.4}, w: 800, h: 600,
			want: [4]float64{0.1, 0.2, 0.3, 0.4},
		},
		{
			name: "exact unit box", in: [4]float64{0, 0, 1, 1}, w: 800, h: 600,
			want: [4]float64{0, 0, 1, 1},
		},
		{
			name: "absolute pixels", in: [4]float64{80, 120, 400, 300}, w: 800, h: 600,
			want: [4]float64{0.1, 0.2, 0.5, 0.5},
		},
		{
			name: "thousandths (Qwen-VL)", in: [4]float64{100, 200, 300, 400}, w: 50, h: 50,
			want: [4]float64{0.1, 0.2, 0.3, 0.4},
			// Too large for a 50x50 canvas, so it must be read as thousandths.
		},
		{
			name: "slightly over the edge still counts as pixels", in: [4]float64{0, 0, 810, 600}, w: 800, h: 600,
			want: [4]float64{0, 0, 1.0125, 1},
		},
		{
			name: "beyond both conventions", in: [4]float64{0, 0, 5000, 5000}, w: 800, h: 600,
			wantErr: true,
		},
		{
			name: "NaN rejected", in: [4]float64{math.NaN(), 0, 1, 1}, w: 800, h: 600,
			wantErr: true,
		},
		{
			name: "Inf rejected", in: [4]float64{0, 0, math.Inf(1), 1}, w: 800, h: 600,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normaliseCoords(tc.in, tc.w, tc.h)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for i := range got {
				approx(t, "coord", got[i], tc.want[i])
			}
		})
	}
}

// End-to-end: a Qwen-style thousandths reply must survive parsing.
func TestParseDetectionsThousandthsReply(t *testing.T) {
	reply := `{"objects":[{"label":"button","bbox_2d":[100,250,300,500],"confidence":0.95}]}`
	dets, err := parseDetections(reply, 200, 200)
	if err != nil {
		t.Fatalf("parseDetections: %v", err)
	}
	approx(t, "X0", dets[0].Box.X0, 0.1)
	approx(t, "Y1", dets[0].Box.Y1, 0.5)
}

// End-to-end: a pixel-coordinate reply must be divided by the image size.
func TestParseDetectionsPixelReply(t *testing.T) {
	reply := `[{"label":"button","bbox":[160,120,320,240]}]`
	dets, err := parseDetections(reply, 1600, 1200)
	if err != nil {
		t.Fatalf("parseDetections: %v", err)
	}
	approx(t, "X0", dets[0].Box.X0, 0.1)
	approx(t, "Y0", dets[0].Box.Y0, 0.1)
	approx(t, "X1", dets[0].Box.X1, 0.2)
	approx(t, "Y1", dets[0].Box.Y1, 0.2)
}

// ---------------------------------------------------------------------------
// matchLabels
// ---------------------------------------------------------------------------

// det builds a detection whose box is derived from the label, so two
// detections are never accidentally equal.
func det(label string, found bool) detection {
	off := float64(len(label)%10) / 100
	return detection{
		Label:      label,
		Box:        geometry.Box{Label: label, X0: 0.1 + off, Y0: 0.1 + off, X1: 0.2 + off, Y1: 0.2 + off},
		Confidence: 0.9,
		Found:      found,
	}
}

func TestMatchLabelsExact(t *testing.T) {
	found := []detection{det("logo", true), det("title", true)}
	matched, missing := matchLabels([]string{"logo", "title"}, found)
	if len(matched) != 2 || len(missing) != 0 {
		t.Fatalf("matched=%d missing=%v", len(matched), missing)
	}
	if matched[0].Label != "logo" || matched[1].Label != "title" {
		t.Errorf("order not preserved: %q %q", matched[0].Label, matched[1].Label)
	}
}

// Models reformat labels freely; canonical matching must absorb that.
func TestMatchLabelsCanonicalForm(t *testing.T) {
	cases := []struct{ requested, returned string }{
		{"screw_A", "Screw A"},
		{"Submit Button", "submit-button"},
		{"logo", "LOGO"},
		{"top_left_icon", "Top Left Icon"},
		{"确认按钮", "确认按钮"},
	}
	for _, tc := range cases {
		t.Run(tc.requested, func(t *testing.T) {
			matched, missing := matchLabels([]string{tc.requested}, []detection{det(tc.returned, true)})
			if len(missing) != 0 {
				t.Fatalf("%q did not match %q", tc.requested, tc.returned)
			}
			// The caller's vocabulary must be echoed back, not the model's.
			if matched[0].Label != tc.requested {
				t.Errorf("label = %q, want the requested %q", matched[0].Label, tc.requested)
			}
			if matched[0].Box.Label != tc.requested {
				t.Errorf("box label = %q, want %q", matched[0].Box.Label, tc.requested)
			}
		})
	}
}

func TestMatchLabelsSubstringFallback(t *testing.T) {
	matched, missing := matchLabels([]string{"logo"}, []detection{det("company logo top left", true)})
	if len(missing) != 0 {
		t.Fatalf("substring fallback failed: missing=%v", missing)
	}
	if matched[0].Label != "logo" {
		t.Errorf("label = %q", matched[0].Label)
	}
}

func TestMatchLabelsMissing(t *testing.T) {
	matched, missing := matchLabels([]string{"logo", "footer"}, []detection{det("logo", true)})
	if len(matched) != 1 {
		t.Errorf("matched = %d, want 1", len(matched))
	}
	if len(missing) != 1 || missing[0] != "footer" {
		t.Errorf("missing = %v, want [footer]", missing)
	}
}

// An object the model explicitly reported as absent counts as missing.
func TestMatchLabelsNotFoundCountsAsMissing(t *testing.T) {
	_, missing := matchLabels([]string{"ghost"}, []detection{det("ghost", false)})
	if len(missing) != 1 {
		t.Errorf("missing = %v, want [ghost]", missing)
	}
}

// Two requests for similar labels must not both consume the same detection.
func TestMatchLabelsDoesNotReuseADetection(t *testing.T) {
	found := []detection{det("button", true), det("button 2", true)}
	matched, missing := matchLabels([]string{"button", "button 2"}, found)
	if len(missing) != 0 {
		t.Fatalf("missing = %v", missing)
	}
	if len(matched) != 2 {
		t.Fatalf("matched = %d, want 2", len(matched))
	}
	if matched[0].Box.X0 == matched[1].Box.X0 && matched[0].Box.Y0 == matched[1].Box.Y0 {
		t.Error("both requested labels resolved to the same detection")
	}
}

func TestCanonical(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Screw A", "screwa"},
		{"screw_A", "screwa"},
		{"submit-button!", "submitbutton"},
		{"  LOGO  ", "logo"},
		{"item 42", "item42"},
		{"", ""},
		{"---", ""},
		{"确认按钮", "确认按钮"}, // non-ASCII preserved
	}
	for _, tc := range cases {
		if got := canonical(tc.in); got != tc.want {
			t.Errorf("canonical(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "a", "b"); got != "a" {
		t.Errorf("got %q, want a", got)
	}
	if got := firstNonEmpty("", "  "); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := firstNonEmpty("  padded  "); got != "padded" {
		t.Errorf("got %q, want trimmed", got)
	}
}
