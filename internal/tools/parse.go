package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/xiaos/llm-eyes-mcp/internal/geometry"
)

// detection is one object located by the VLM, before label reconciliation.
type detection struct {
	Label      string
	Box        geometry.Box
	Confidence float64
	// Found is false when the model explicitly reported the object as absent.
	Found bool
}

// rawDetection tolerates the various shapes models actually emit.
type rawDetection struct {
	Label       string          `json:"label"`
	Name        string          `json:"name"`
	Object      string          `json:"object"`
	BBox        json.RawMessage `json:"bbox"`
	Box         json.RawMessage `json:"box"`
	BoundingBox json.RawMessage `json:"bounding_box"`
	Bbox2D      json.RawMessage `json:"bbox_2d"`
	Confidence  *float64        `json:"confidence"`
	Score       *float64        `json:"score"`
}

type rawEnvelope struct {
	Objects    []rawDetection `json:"objects"`
	Detections []rawDetection `json:"detections"`
	Results    []rawDetection `json:"results"`
	Items      []rawDetection `json:"items"`
}

var fenceRe = regexp.MustCompile("(?s)```(?:json|JSON)?\\s*(.*?)\\s*```")

// extractJSON pulls a JSON document out of a model reply that may be wrapped in
// markdown fences or padded with prose.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = strings.TrimSpace(m[1])
	}
	if s == "" {
		return ""
	}
	if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
		(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
		return s
	}
	// Fall back to the widest balanced-looking span.
	objStart, objEnd := strings.Index(s, "{"), strings.LastIndex(s, "}")
	arrStart, arrEnd := strings.Index(s, "["), strings.LastIndex(s, "]")
	switch {
	case arrStart >= 0 && arrEnd > arrStart && (objStart < 0 || arrStart < objStart):
		return s[arrStart : arrEnd+1]
	case objStart >= 0 && objEnd > objStart:
		return s[objStart : objEnd+1]
	}
	return s
}

// parseDetections decodes the VLM reply into normalised boxes.
//
// imgW/imgH are the dimensions of the image the model actually saw (the
// preprocessed/resized one), used to disambiguate pixel vs thousandths vs
// normalised coordinates; see normaliseCoords.
func parseDetections(reply string, imgW, imgH int) ([]detection, error) {
	doc := extractJSON(reply)
	if doc == "" {
		return nil, fmt.Errorf("model returned no JSON")
	}

	var raws []rawDetection
	if strings.HasPrefix(doc, "[") {
		if err := json.Unmarshal([]byte(doc), &raws); err != nil {
			return nil, fmt.Errorf("parse JSON array: %w", err)
		}
	} else {
		var env rawEnvelope
		if err := json.Unmarshal([]byte(doc), &env); err != nil {
			return nil, fmt.Errorf("parse JSON object: %w", err)
		}
		switch {
		case len(env.Objects) > 0:
			raws = env.Objects
		case len(env.Detections) > 0:
			raws = env.Detections
		case len(env.Results) > 0:
			raws = env.Results
		case len(env.Items) > 0:
			raws = env.Items
		default:
			// A single bare detection object.
			var one rawDetection
			if err := json.Unmarshal([]byte(doc), &one); err == nil && one.anyBox() != nil {
				raws = []rawDetection{one}
			}
		}
	}
	if len(raws) == 0 {
		return nil, fmt.Errorf("model returned no objects")
	}

	out := make([]detection, 0, len(raws))
	for _, r := range raws {
		label := firstNonEmpty(r.Label, r.Name, r.Object)
		if label == "" {
			continue
		}
		coords, err := decodeBox(r.anyBox())
		if err != nil {
			continue
		}
		conf := 0.0
		if r.Confidence != nil {
			conf = *r.Confidence
		} else if r.Score != nil {
			conf = *r.Score
		}
		nx, err := normaliseCoords(coords, imgW, imgH)
		if err != nil {
			continue
		}
		degenerate := nx[0] == 0 && nx[1] == 0 && nx[2] == 0 && nx[3] == 0
		out = append(out, detection{
			Label:      strings.TrimSpace(label),
			Box:        geometry.Box{Label: strings.TrimSpace(label), X0: nx[0], Y0: nx[1], X1: nx[2], Y1: nx[3], Confidence: conf},
			Confidence: conf,
			// Rule 5 of the system prompt: absent objects come back as an
			// all-zero box with confidence 0.
			Found: !(degenerate || conf < 0),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("model returned objects but none had a usable bounding box")
	}
	return out, nil
}

func (r rawDetection) anyBox() json.RawMessage {
	for _, b := range []json.RawMessage{r.BBox, r.Box, r.BoundingBox, r.Bbox2D} {
		if len(b) > 0 && string(b) != "null" {
			return b
		}
	}
	return nil
}

// decodeBox accepts [x0,y0,x1,y1] or {"x0":..,"y0":..,"x1":..,"y1":..} or
// {"x":..,"y":..,"width":..,"height":..}.
func decodeBox(raw json.RawMessage) ([4]float64, error) {
	var zero [4]float64
	if len(raw) == 0 {
		return zero, fmt.Errorf("no bbox field")
	}
	var arr []float64
	if err := json.Unmarshal(raw, &arr); err == nil {
		if len(arr) < 4 {
			return zero, fmt.Errorf("bbox needs 4 numbers, got %d", len(arr))
		}
		return [4]float64{arr[0], arr[1], arr[2], arr[3]}, nil
	}
	var obj struct {
		X0, Y0, X1, Y1 *float64
		Left           *float64 `json:"left"`
		Top            *float64 `json:"top"`
		Right          *float64 `json:"right"`
		Bottom         *float64 `json:"bottom"`
		X              *float64 `json:"x"`
		Y              *float64 `json:"y"`
		Width          *float64 `json:"width"`
		Height         *float64 `json:"height"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return zero, fmt.Errorf("unsupported bbox shape")
	}
	switch {
	case obj.X0 != nil && obj.Y0 != nil && obj.X1 != nil && obj.Y1 != nil:
		return [4]float64{*obj.X0, *obj.Y0, *obj.X1, *obj.Y1}, nil
	case obj.Left != nil && obj.Top != nil && obj.Right != nil && obj.Bottom != nil:
		return [4]float64{*obj.Left, *obj.Top, *obj.Right, *obj.Bottom}, nil
	case obj.X != nil && obj.Y != nil && obj.Width != nil && obj.Height != nil:
		return [4]float64{*obj.X, *obj.Y, *obj.X + *obj.Width, *obj.Y + *obj.Height}, nil
	}
	return zero, fmt.Errorf("unsupported bbox shape")
}

// normaliseCoords converts whatever coordinate convention the model used into
// [0,1].
//
// imgW/imgH MUST be the dimensions of the image the model actually saw (the
// preprocessed/resized one), NOT the original: pixel coordinates are relative
// to what was on the model's screen, so dividing them by the original size
// would shrink every box.
//
// Three conventions appear in the wild despite an explicit instruction:
//   - [0,1] floats            (what we asked for)
//   - [0,1000] thousandths    (Qwen-VL family)
//   - absolute pixels         (several fine-tunes)
//
// They are told apart by magnitude. When a value fits both the pixel range and
// the thousandths range the pixel reading wins, since a model emitting pixels
// for a small image is more common than thousandths that happen to land inside
// the canvas. This leaves a residual ambiguity when the seen dimension is
// >= ~1000px: thousandths (0..1000) then also fit the pixel range and get
// misread as pixels. That case cannot be disambiguated from the values alone -
// it would need an out-of-band coordinate-convention hint.
func normaliseCoords(c [4]float64, imgW, imgH int) ([4]float64, error) {
	for _, v := range c {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < -1e6 || v > 1e6 {
			return c, fmt.Errorf("coordinate out of range")
		}
	}
	maxV := math.Max(math.Max(c[0], c[1]), math.Max(c[2], c[3]))
	if maxV <= 1.5 {
		return c, nil
	}

	w, h := float64(imgW), float64(imgH)
	fitsPixels := w > 0 && h > 0 &&
		c[0] <= w*1.02 && c[2] <= w*1.02 && c[1] <= h*1.02 && c[3] <= h*1.02
	fitsThousandths := maxV <= 1000.5

	switch {
	case fitsPixels:
		return [4]float64{c[0] / w, c[1] / h, c[2] / w, c[3] / h}, nil
	case fitsThousandths:
		return [4]float64{c[0] / 1000, c[1] / 1000, c[2] / 1000, c[3] / 1000}, nil
	default:
		return c, fmt.Errorf("coordinates %v fit neither pixel nor normalised range for a %dx%d image", c, imgW, imgH)
	}
}

// matchLabels reconciles requested labels with what the model returned. Models
// reformat labels freely ("screw_A" -> "Screw A"), so matching is done on a
// canonical form before falling back to substring containment.
//
// It returns one detection per requested label (in request order) plus the list
// of labels that could not be resolved.
func matchLabels(requested []string, found []detection) (matched []detection, missing []string) {
	used := make([]bool, len(found))

	pick := func(want string) (detection, bool) {
		cw := canonical(want)
		// Pass 1: exact canonical match.
		for i, d := range found {
			if !used[i] && canonical(d.Label) == cw {
				used[i] = true
				return d, true
			}
		}
		// Pass 2: containment either way.
		for i, d := range found {
			if used[i] {
				continue
			}
			cd := canonical(d.Label)
			if cd == "" || cw == "" {
				continue
			}
			if strings.Contains(cd, cw) || strings.Contains(cw, cd) {
				used[i] = true
				return d, true
			}
		}
		return detection{}, false
	}

	for _, want := range requested {
		d, ok := pick(want)
		if !ok || !d.Found {
			missing = append(missing, want)
			continue
		}
		// Report under the label the caller asked for, not the model's variant,
		// so downstream summaries echo the user's own vocabulary.
		d.Label = want
		d.Box.Label = want
		matched = append(matched, d)
	}
	return matched, missing
}

// canonical lowercases and strips every non-alphanumeric rune.
func canonical(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 127 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
