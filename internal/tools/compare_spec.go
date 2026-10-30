package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// compareSpec mirrors docs/spec-schema.md. Kept small and flat to avoid
// coupling to any single D2C tool.
type compareSpec struct {
	Version int             `json:"version"`
	Image   *specImage      `json:"image,omitempty"`
	Regions []compareRegion `json:"regions"`
}

type specImage struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type compareRegion struct {
	ID    string    `json:"id"`
	Desc  string    `json:"desc,omitempty"`
	BBox  []float64 `json:"bbox,omitempty"`
	Color string    `json:"color,omitempty"`
	Text  string    `json:"text,omitempty"`
}

func (r compareRegion) hasExpectation() bool {
	return len(r.BBox) == 4 || r.Color != "" || r.Text != ""
}

func loadSpec(src string) (*compareSpec, string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		// Fallback: treat src as inline JSON.
		data = []byte(src)
	}
	return parseSpec(data)
}

func parseSpec(data []byte) (*compareSpec, string, error) {
	var spec compareSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, "", fmt.Errorf("spec parse failed: %w", err)
	}
	h := sha256.Sum256(data)
	return &spec, hex.EncodeToString(h[:]), nil
}

func validateSpec(s *compareSpec) error {
	if s.Version != 1 {
		return fmt.Errorf("unsupported version %d (expected 1)", s.Version)
	}
	if len(s.Regions) == 0 {
		return fmt.Errorf("spec requires at least 1 region")
	}
	if len(s.Regions) > 50 {
		return fmt.Errorf("spec has %d regions (max 50); split into smaller components", len(s.Regions))
	}
	seen := map[string]bool{}
	for i, r := range s.Regions {
		if r.ID == "" {
			return fmt.Errorf("region[%d] missing id", i)
		}
		if seen[r.ID] {
			return fmt.Errorf("duplicate region id %q", r.ID)
		}
		seen[r.ID] = true
		if len(r.BBox) > 0 && len(r.BBox) != 4 {
			return fmt.Errorf("region %q bbox has %d coords (expect 4)", r.ID, len(r.BBox))
		}
		if r.Color != "" && !hexColorRe.MatchString(r.Color) {
			return fmt.Errorf("region %q bad color %q", r.ID, r.Color)
		}
		if !r.hasExpectation() {
			return fmt.Errorf("region %q has none of bbox/color/text; meaningless to compare", r.ID)
		}
	}
	return nil
}

var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
