package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xiaos/llm-eyes-mcp/internal/cache"
	"github.com/xiaos/llm-eyes-mcp/internal/geometry"
	"github.com/xiaos/llm-eyes-mcp/internal/imageio"
	"github.com/xiaos/llm-eyes-mcp/internal/imgproc"
	"github.com/xiaos/llm-eyes-mcp/internal/mcp"
	"github.com/xiaos/llm-eyes-mcp/internal/vlm"
)

// MeasureTool is the hard-pipeline entry point: the VLM only supplies
// coordinates, and Go computes every number that is returned.
type MeasureTool struct{ d *Deps }

// NewMeasureTool builds the measurement tool.
func NewMeasureTool(d *Deps) *MeasureTool { return &MeasureTool{d: d} }

func (t *MeasureTool) Name() string { return "measure_image" }

func (t *MeasureTool) Description() string {
	return "Execute precise geometric measurement on an image and return exact numbers in pixels. " +
		"Use this ONLY when the user explicitly asks for a distance, size comparison, alignment or centering check, overlap, or area calculation. " +
		"Do NOT use it for semantic understanding, style analysis, or reading text. " +
		"A vision model supplies bounding boxes and all arithmetic is performed deterministically in code, so results are reproducible rather than estimated. " +
		"Measurements are in pixels of the original image; physical units (mm, inches) require a calibration target and are not supported."
}

func (t *MeasureTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"image_source": map[string]any{
				"type": "string",
				"description": "The image to measure. Accepts an http(s) URL, an absolute file path, a file:// URI, a data URI, raw base64, " +
					"or a 32-character image_id returned by a previous call (cheapest option: skips re-upload and hits the cache).",
			},
			"measure_type": map[string]any{
				"type": "string",
				"enum": []string{"distance", "alignment", "area"},
				"description": "distance = center-to-center and nearest-edge distance between exactly 2 targets. " +
					"alignment = centering of 1 target within the image, or mutual edge/center alignment of 2 or more targets. " +
					"area = bounding-box area in square pixels plus percentage of the image.",
			},
			"target_labels": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "Names of the objects to locate, e.g. [\"screw_A\", \"screw_B\"]. Exactly 2 are required for distance; " +
					"1 or more for alignment and area. Use concrete, visually identifiable nouns.",
			},
			"refresh": map[string]any{
				"type":        "boolean",
				"description": "Optional: set true to bypass the cache and re-run the vision model. Use when the user says the previous reading was wrong or asks to re-analyse.",
			},
			"provider": map[string]any{
				"type":        "string",
				"description": "Optional: name of a specific configured vision provider. Defaults to the server's default provider.",
			},
		},
		"required": []string{"image_source", "measure_type", "target_labels"},
	}
}

// measurePayload is the cacheable, renderable result of one measurement.
type measurePayload struct {
	Value       float64        `json:"value"`
	Unit        string         `json:"unit"`
	Summary     string         `json:"summary"`
	MeasureType string         `json:"measure_type"`
	ImageID     string         `json:"image_id"`
	ImageWidth  int            `json:"image_width"`
	ImageHeight int            `json:"image_height"`
	Details     map[string]any `json:"details"`
	Model       string         `json:"model,omitempty"`
	CacheTier   string         `json:"cache_tier"`
	Rounds      int            `json:"vlm_rounds,omitempty"`
	Confidence  []labelConf    `json:"label_confidence,omitempty"`
}

type labelConf struct {
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
}

// measureCacheEntry wraps a cached VLM reply with the dimensions of the image
// the model actually saw (the preprocessed/resized one). Pixel coordinates in
// the reply are relative to THAT image, so re-parsing a cached reply needs the
// same dims. Storing them makes the entry self-describing: no coupling to the
// preprocessor's current sizing, and a future change to HardMaxEdge cannot
// silently mis-parse old entries.
type measureCacheEntry struct {
	Text string `json:"text"`
	W    int    `json:"w"`
	H    int    `json:"h"`
}

// decodeMeasureCache unwraps a cached entry. Returns nil for anything that is
// not a valid envelope - including legacy raw-text entries, which simply get
// discarded by the caller so the next call repopulates the slot.
func decodeMeasureCache(blob []byte) *measureCacheEntry {
	var e measureCacheEntry
	if json.Unmarshal(blob, &e) == nil && e.Text != "" && e.W > 0 && e.H > 0 {
		return &e
	}
	return nil
}

// Call runs the hard pipeline: load -> coordinates (cached) -> Go arithmetic.
func (t *MeasureTool) Call(ctx context.Context, args map[string]any) *mcp.ToolResult {
	source := imageSourceArg(args)
	if source == "" {
		return mcp.ErrorResult("image_source is required: pass a URL, file path, data URI, base64 string, or a previously returned image_id.")
	}
	measureType := strings.ToLower(getString(args, "measure_type"))
	switch measureType {
	case "distance", "alignment", "area":
	case "":
		return mcp.ErrorResult("measure_type is required and must be one of: distance, alignment, area.")
	default:
		return mcp.ErrorResult(fmt.Sprintf("Unsupported measure_type %q. Valid values: distance, alignment, area.", measureType))
	}

	labels := getStringSlice(args, "target_labels")
	if len(labels) == 0 {
		return mcp.ErrorResult(`target_labels is required: name at least one object to locate, e.g. ["screw_A", "screw_B"].`)
	}
	if measureType == "distance" && len(labels) != 2 {
		return mcp.ErrorResult(fmt.Sprintf(
			"measure_type=distance requires exactly 2 target_labels, got %d (%s). Distance is defined between two objects.",
			len(labels), strings.Join(labels, ", ")))
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

	// L3: this exact measurement on this exact image is arithmetic we already
	// performed. Serving it costs nothing and involves no network at all. The
	// key carries the model version so a different provider gets its own entry
	// instead of silently inheriting another provider's geometry.
	l3Key := cache.L3Key(img.ID, measureType, provider.ModelVersion(), paramKey(map[string]string{
		"labels": strings.Join(sortedCopy(labels), "|"),
	}))
	if !refresh && t.d.Cache != nil {
		if blob, ok := t.d.Cache.L3.Get(l3Key); ok {
			var cached measurePayload
			if json.Unmarshal(blob, &cached) == nil {
				cached.CacheTier = "L3"
				t.d.logf("L3 hit for %s/%s", img.ID[:8], measureType)
				return renderMeasure(&cached)
			}
		}
	}

	dets, meta, err := t.d.detectObjects(ctx, img, provider, labels, measureType, refresh)
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}

	// Normalised coordinates are resolution independent, so they are scaled by
	// the ORIGINAL dimensions. Every reported pixel value therefore refers to
	// the image the user actually supplied, not the downscaled VLM input.
	boxes := make([]geometry.PixelBox, 0, len(dets))
	confs := make([]labelConf, 0, len(dets))
	for _, d := range dets {
		boxes = append(boxes, d.Box.Scale(img.Width, img.Height))
		confs = append(confs, labelConf{Label: d.Label, Confidence: d.Confidence})
	}

	var res geometry.Result
	switch measureType {
	case "distance":
		res = geometry.Distance(boxes[0], boxes[1])
	case "area":
		res = geometry.Area(boxes, img.Width, img.Height)
	case "alignment":
		res = geometry.Alignment(boxes, img.Width, img.Height)
	}

	payload := &measurePayload{
		Value:       res.Value,
		Unit:        res.Unit,
		Summary:     res.Summary,
		MeasureType: measureType,
		ImageID:     img.ID,
		ImageWidth:  img.Width,
		ImageHeight: img.Height,
		Details:     res.Details,
		Model:       meta.model,
		CacheTier:   meta.tier,
		Rounds:      meta.rounds,
		Confidence:  confs,
	}
	if t.d.Cache != nil {
		if blob, err := json.Marshal(payload); err == nil {
			t.d.Cache.L3.Put(l3Key, blob)
		}
	}
	return renderMeasure(payload)
}

// renderMeasure builds the tool response: a human-readable headline first, then
// the full structured payload for the LLM to reason over.
//
// Note what is NOT included: the raw VLM reply. Only numbers survive into the
// final synthesis step, which keeps intermediate model prose out of the
// context window.
func renderMeasure(p *measurePayload) *mcp.ToolResult {
	headline := fmt.Sprintf("%s = %.2f %s\n%s", p.MeasureType, p.Value, p.Unit, p.Summary)
	notes := []string{
		fmt.Sprintf("image_id=%s (%dx%d px)", p.ImageID, p.ImageWidth, p.ImageHeight),
		"cache=" + p.CacheTier,
	}
	if p.Model != "" {
		notes = append(notes, "model="+p.Model)
	}
	if low := lowConfidence(p.Confidence); len(low) > 0 {
		notes = append(notes, "LOW CONFIDENCE for "+strings.Join(low, ", ")+
			" - treat the number as an estimate and consider refresh=true")
	}
	notes = append(notes, "Values are pixels of the original image. Physical units require a calibration target.")
	headline += "\n[" + strings.Join(notes, "; ") + "]"

	blob, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return mcp.TextResult(headline)
	}
	return &mcp.ToolResult{Content: []map[string]any{
		mcp.TextContent(headline),
		mcp.TextContent("Structured result:\n" + string(blob)),
	}}
}

func lowConfidence(cs []labelConf) []string {
	var out []string
	for _, c := range cs {
		// 0 means the model did not report a score at all; only flag an
		// explicitly low score.
		if c.Confidence > 0 && c.Confidence < 0.4 {
			out = append(out, fmt.Sprintf("%q(%.2f)", c.Label, c.Confidence))
		}
	}
	return out
}

// detectMeta records where the coordinates came from.
type detectMeta struct {
	tier   string // "L2" (cached) or "vlm" (fresh call)
	model  string
	rounds int
}

// detectObjects returns one bounding box per requested label.
//
// Convergence is capped at Deps.MaxRounds (default 2): round 1 asks normally,
// round 2 re-asks naming exactly what was missing. Beyond that we give up and
// report the failure so the agent can rename its targets rather than let the
// server loop and burn tokens.
func (d *Deps) detectObjects(
	ctx context.Context,
	img *imageio.Image,
	provider vlm.Provider,
	labels []string,
	measureType string,
	refresh bool,
) ([]detection, detectMeta, error) {
	meta := detectMeta{tier: "L2", model: provider.ModelVersion()}

	paramHash := cache.ParamHash(map[string]string{
		"labels": strings.Join(sortedCopy(labels), "|"),
	})
	l2Key := cache.L2Key(img.ID, "measure_image", provider.ModelVersion(), paramHash)

	if d.Cache != nil {
		if refresh {
			// Forced re-analysis: drop the stale entry before re-querying.
			d.Cache.L2.Delete(l2Key)
		} else if blob, ok := d.Cache.L2.Get(l2Key); ok {
			if e := decodeMeasureCache(blob); e != nil {
				// Re-parse with the dims the model saw when the entry was
				// written, not the current original dims.
				if dets, err := parseDetections(e.Text, e.W, e.H); err == nil {
					matched, missing := matchLabels(labels, dets)
					if len(missing) == 0 {
						d.logf("L2 hit for %s/measure_image", img.ID[:8])
						return matched, meta, nil
					}
				}
			}
			// A cached reply that no longer parses is worthless; discard it.
			d.Cache.L2.Delete(l2Key)
		}
	}

	// Hard pipeline: lossless downscale to <=1500px + CLAHE + unsharp. JPEG is
	// forbidden here because ringing artefacts around high-contrast edges would
	// corrupt the very coordinates we are about to measure.
	proc, err := d.preprocess(img, imgproc.HardOptions())
	if err != nil {
		return nil, meta, fmt.Errorf("preprocess image for measurement: %w", err)
	}

	meta.tier = "vlm"
	var lastReply string
	var lastErr error
	maxRounds := d.maxRounds()

	for round := 1; round <= maxRounds; round++ {
		meta.rounds = round
		prompt := measurePrompt(labels, measureType)
		if round > 1 {
			_, missing := lastMissing(labels, lastReply, proc.Width, proc.Height)
			prompt = measureRetryPrompt(missing, lastReply)
		}

		resp, err := provider.Complete(ctx, vlm.Request{
			System:    measureSystemPrompt,
			Prompt:    prompt,
			Image:     proc.Data,
			ImageMIME: proc.MIMEType,
			JSONOnly:  true,
		})
		if err != nil {
			lastErr = err
			d.logf("measure round %d/%d failed: %v", round, maxRounds, err)
			continue
		}
		meta.model = resp.Model
		lastReply = resp.Text

		// Parse with the dims of the image the model actually SAW (the resized
		// proc output), not the original: pixel coordinates are relative to
		// what was on the model's screen.
		dets, err := parseDetections(resp.Text, proc.Width, proc.Height)
		if err != nil {
			lastErr = err
			d.logf("measure round %d/%d unparsable: %v", round, maxRounds, err)
			continue
		}
		matched, missing := matchLabels(labels, dets)
		if len(missing) == 0 {
			if d.Cache != nil {
				if blob, err := json.Marshal(measureCacheEntry{Text: resp.Text, W: proc.Width, H: proc.Height}); err == nil {
					if err := d.Cache.L2.Put(l2Key, blob); err != nil {
						d.logf("warning: write L2: %v", err)
					}
				}
			}
			return matched, meta, nil
		}
		lastErr = fmt.Errorf("vision model did not locate: %s", strings.Join(missing, ", "))
		d.logf("measure round %d/%d missing labels: %s", round, maxRounds, strings.Join(missing, ", "))
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no usable coordinates returned")
	}
	return nil, meta, fmt.Errorf(
		"Measurement failed after %d round(s): %v. Try labels that name clearly visible, distinct objects, or call describe_image first to learn what is actually in the picture.",
		maxRounds, lastErr)
}

// lastMissing re-derives which labels were absent from a previous reply, so the
// retry prompt can name them explicitly. w/h are the dims the model saw.
func lastMissing(labels []string, reply string, w, h int) ([]detection, []string) {
	if reply == "" {
		return nil, labels
	}
	dets, err := parseDetections(reply, w, h)
	if err != nil {
		return nil, labels
	}
	return matchLabels(labels, dets)
}
