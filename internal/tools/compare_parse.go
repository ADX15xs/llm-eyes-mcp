package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xiaos/llm-eyes-mcp/internal/geometry"
)

type rawRegion struct {
	ID          string          `json:"id"`
	Label       string          `json:"label"`
	Name        string          `json:"name"`
	BBox        json.RawMessage `json:"bbox"`
	Box         json.RawMessage `json:"box"`
	BoundingBox json.RawMessage `json:"bounding_box"`
	Bbox2D      json.RawMessage `json:"bbox_2d"`
	Text        string          `json:"text"`
	Confidence  *float64        `json:"confidence"`
	Score       *float64        `json:"score"`
}
type rawRegionEnvelope struct {
	Regions []rawRegion `json:"regions"`
	Objects []rawRegion `json:"objects"`
	Results []rawRegion `json:"results"`
	Items   []rawRegion `json:"items"`
}

func parseRegionReply(reply string, imgW, imgH int) ([]regionDetection, error) {
	doc := extractJSON(reply)
	if doc == "" {
		return nil, fmt.Errorf("model returned no JSON")
	}
	var raws []rawRegion
	if strings.HasPrefix(doc, "[") {
		if err := json.Unmarshal([]byte(doc), &raws); err != nil {
			return nil, fmt.Errorf("parse JSON array: %w", err)
		}
	} else {
		var env rawRegionEnvelope
		if err := json.Unmarshal([]byte(doc), &env); err != nil {
			return nil, fmt.Errorf("parse JSON object: %w", err)
		}
		switch {
		case len(env.Regions) > 0:
			raws = env.Regions
		case len(env.Objects) > 0:
			raws = env.Objects
		case len(env.Results) > 0:
			raws = env.Results
		case len(env.Items) > 0:
			raws = env.Items
		default:
			var one rawRegion
			if err := json.Unmarshal([]byte(doc), &one); err == nil && one.anyBox() != nil {
				raws = []rawRegion{one}
			}
		}
	}
	if len(raws) == 0 {
		return nil, fmt.Errorf("model returned no regions")
	}

	out := make([]regionDetection, 0, len(raws))
	for _, r := range raws {
		id := firstNonEmpty(r.ID, r.Label, r.Name)
		if id == "" {
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
		out = append(out, regionDetection{
			ID:         strings.TrimSpace(id),
			Box:        geometry.Box{Label: strings.TrimSpace(id), X0: nx[0], Y0: nx[1], X1: nx[2], Y1: nx[3]},
			Text:       r.Text,
			Confidence: conf,
			Found:      !degenerate,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("model returned regions but none had a usable bbox")
	}
	return out, nil
}

func (r rawRegion) anyBox() json.RawMessage {
	for _, b := range []json.RawMessage{r.BBox, r.Box, r.BoundingBox, r.Bbox2D} {
		if len(b) > 0 && string(b) != "null" {
			return b
		}
	}
	return nil
}

func lastMissingIDs(spec *compareSpec, reply string, w, h int) []string {
	if reply == "" {
		return specIDs(spec)
	}
	dets, err := parseRegionReply(reply, w, h)
	if err != nil {
		return specIDs(spec)
	}
	found := map[string]bool{}
	for _, d := range dets {
		found[d.ID] = true
	}
	var missing []string
	for _, r := range spec.Regions {
		if !found[r.ID] {
			missing = append(missing, r.ID)
		}
	}
	return missing
}

func specIDs(spec *compareSpec) []string {
	out := make([]string, len(spec.Regions))
	for i, r := range spec.Regions {
		out[i] = r.ID
	}
	return out
}
