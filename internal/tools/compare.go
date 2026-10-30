package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/xiaos/llm-eyes-mcp/internal/cache"
	"github.com/xiaos/llm-eyes-mcp/internal/imgproc"
	"github.com/xiaos/llm-eyes-mcp/internal/mcp"
)

// comparePayload is the cacheable, renderable result of one comparison.
type comparePayload struct {
	Summary     string       `json:"summary"`
	Score       float64      `json:"score"` // ok / total, Go-computed
	Total       int          `json:"regions_total"`
	OK          int          `json:"regions_ok"`
	ImageID     string       `json:"image_id"`
	ImageWidth  int          `json:"image_width"`
	ImageHeight int          `json:"image_height"`
	SpecHash    string       `json:"spec_hash"`
	Regions     []regionDiff `json:"regions"`
	Model       string       `json:"model,omitempty"`
	CacheTier   string       `json:"cache_tier"`
	Rounds      int          `json:"vlm_rounds,omitempty"`
}

// CompareTool diffs a structured design spec (machine-readable design truth)
// against a screenshot of the implemented page. The VLM only locates regions
// and transcribes their text; Go computes every comparison number.
//
// Design contract:
//   - Spec is a JSON file produced by an Agent-side converter (e.g. the sloth
//     HTML → spec reference script), not by llm-eyes. We do not know any
//     D2C tool's private format.
//   - The screenshot must be component-level granularity (see docs/spec-schema.md).
//   - All measurement numbers come from Go, never from the model.
type CompareTool struct{ d *Deps }

// NewCompareTool builds the compare tool.
func NewCompareTool(d *Deps) *CompareTool { return &CompareTool{d: d} }

func (t *CompareTool) Name() string { return "compare_design" }

func (t *CompareTool) Description() string {
	return "Compare a structured design spec against an actual screenshot and report per-region restore fidelity. " +
		"The spec is a JSON file or inline object produced by a D2C exporter converter (reference script at examples/sloth_to_spec.py). " +
		"The VLM only locates regions and transcribes text inside them; Go computes colour delta, IoU, and text equality. " +
		"Use this AFTER implementing a design component from a D2C export, on a CROPPED screenshot of ONE component " +
		"together with the spec slice for that component. " +
		"Do NOT use it for open-ended image questions (use describe_image), geometry between arbitrary objects (use measure_image), or full-page comparisons. " +
		"See docs/spec-schema.md for the spec contract."
}

func (t *CompareTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"image_source": map[string]any{
				"type":        "string",
				"description": "The screenshot to compare. Accepts an http(s) URL, an absolute file path, a file:// URI, a data URI, raw base64, or a previously returned image_id.",
			},
			"spec": map[string]any{
				"type":        []any{"string", "object"},
				"description": "Path to the spec JSON file, the inline JSON string, or the spec JSON object itself. See docs/spec-schema.md for the format.",
			},
			"refresh": map[string]any{
				"type":        "boolean",
				"description": "Optional: set true to bypass the cache and re-run the vision model.",
			},
			"provider": map[string]any{
				"type":        "string",
				"description": "Optional: name of a specific configured vision provider. Defaults to the server's default provider.",
			},
		},
		"required": []string{"image_source", "spec"},
	}
}

// Call runs the hard pipeline: spec parse -> VLM locate -> Go compare.
func (t *CompareTool) Call(ctx context.Context, args map[string]any) *mcp.ToolResult {
	source := imageSourceArg(args)
	if source == "" {
		return mcp.ErrorResult("image_source is required: pass a URL, file path, data URI, base64 string, or a previously returned image_id.")
	}
	specSrc := strings.TrimSpace(getString(args, "spec"))
	var spec *compareSpec
	var specHash string
	var err error
	switch v := args["spec"].(type) {
	case nil:
		return mcp.ErrorResult("spec is required: pass a JSON file path, an inline JSON string, or an inline object.")
	case map[string]any:
		data, mErr := json.Marshal(v)
		if mErr != nil {
			return mcp.ErrorResult("failed to encode spec object: " + mErr.Error())
		}
		spec, specHash, err = parseSpec(data)
	default:
		if specSrc == "" {
			return mcp.ErrorResult("spec is required: pass a JSON file path, an inline JSON string, or an inline object.")
		}
		spec, specHash, err = loadSpec(specSrc)
	}
	if err != nil {
		return mcp.ErrorResult("failed to parse spec: " + err.Error())
	}
	if err := validateSpec(spec); err != nil {
		return mcp.ErrorResult("invalid spec: " + err.Error())
	}

	img, err := t.d.resolveImage(source)
	if err != nil {
		return mcp.ErrorResult("Failed to load image: " + err.Error())
	}
	provider, err := t.d.providerFor(args)
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}
	refresh := getBool(args, "refresh")

	// L2 key covers image + spec content + provider.
	l2Key := cache.L2Key(img.ID, "compare_design", provider.ModelVersion(),
		cache.ParamHash(map[string]string{"spec_hash": specHash}))

	if d := t.d.Cache; d != nil && !refresh {
		if blob, ok := d.L2.Get(l2Key); ok {
			var cached comparePayload
			if json.Unmarshal(blob, &cached) == nil {
				cached.CacheTier = "L2"
				t.d.logf("compare L2 hit for %s", img.ID[:8])
				return renderCompare(&cached)
			}
		}
	}

	// Hard pipeline: preserve edges for VLM locator + OCR clarity.
	proc, err := t.d.preprocess(img, imgproc.HardOptions())
	if err != nil {
		return mcp.ErrorResult("Failed to preprocess image: " + err.Error())
	}

	dets, meta, err := t.d.locateRegions(ctx, img.ID, proc, provider, spec, specHash, refresh)
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}

	diff := t.diffRegions(spec, img, dets)
	payload := &comparePayload{
		Summary:     summarize(diff),
		Score:       float64(diff.ok) / float64(len(diff.regions)),
		Total:       len(diff.regions),
		OK:          diff.ok,
		ImageID:     img.ID,
		ImageWidth:  img.Width,
		ImageHeight: img.Height,
		SpecHash:    specHash,
		Regions:     diff.regions,
		Model:       meta.model,
		CacheTier:   meta.tier,
		Rounds:      meta.rounds,
	}

	if d := t.d.Cache; d != nil {
		if blob, err := json.Marshal(payload); err == nil {
			d.L2.Put(l2Key, blob)
		}
	}
	return renderCompare(payload)
}
