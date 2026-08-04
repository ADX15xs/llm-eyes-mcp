package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/xiaos/llm-eyes-mcp/internal/cache"
	"github.com/xiaos/llm-eyes-mcp/internal/imgproc"
	"github.com/xiaos/llm-eyes-mcp/internal/mcp"
	"github.com/xiaos/llm-eyes-mcp/internal/vlm"
)

// noTextSentinel is the exact reply the OCR prompt demands for a blank image.
const noTextSentinel = "NO_TEXT_FOUND"

// ExtractTool is the OCR entry point. It runs on the hard pipeline: character
// strokes are high-frequency detail and do not survive aggressive compression.
type ExtractTool struct{ d *Deps }

// NewExtractTool builds the OCR tool.
func NewExtractTool(d *Deps) *ExtractTool { return &ExtractTool{d: d} }

func (t *ExtractTool) Name() string { return "extract_text" }

func (t *ExtractTool) Description() string {
	return "Extract and return all human-readable text content from an image via OCR, preserving reading order and table structure. " +
		"Use this ONLY when the user explicitly asks to read text, numbers, labels, or tables from an image. " +
		"Do NOT use it for measurements (use measure_image) or for describing what the image looks like (use describe_image). " +
		"Best suited to dense documents: invoices, financial statements, reports, forms and screenshots."
}

func (t *ExtractTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"image_source": map[string]any{
				"type": "string",
				"description": "The image to read. Accepts an http(s) URL, an absolute file path, a file:// URI, a data URI, raw base64, " +
					"or a 32-character image_id returned by a previous call (cheapest option: skips re-upload and hits the cache).",
			},
			"region_hint": map[string]any{
				"type":        "string",
				"description": "Optional: describe which part of the image matters, e.g. \"the total row at the bottom\" or \"the serial number on the label\". Narrows the transcription.",
			},
			"refresh": map[string]any{
				"type":        "boolean",
				"description": "Optional: set true to bypass the cache and re-run OCR.",
			},
			"provider": map[string]any{
				"type":        "string",
				"description": "Optional: name of a specific configured vision provider. Defaults to the server's default provider.",
			},
		},
		"required": []string{"image_source"},
	}
}

// Call runs OCR through the hard pipeline.
func (t *ExtractTool) Call(ctx context.Context, args map[string]any) *mcp.ToolResult {
	source := imageSourceArg(args)
	if source == "" {
		return mcp.ErrorResult("image_source is required: pass a URL, file path, data URI, base64 string, or a previously returned image_id.")
	}
	hint := getString(args, "region_hint")

	img, err := t.d.resolveImage(source)
	if err != nil {
		return mcp.ErrorResult("Failed to load image: " + err.Error())
	}
	provider, err := t.d.providerFor(args)
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}
	refresh := getBool(args, "refresh")

	l2Key := cache.L2Key(img.ID, "extract_text", provider.ModelVersion(),
		cache.ParamHash(map[string]string{"hint": hint}))

	if t.d.Cache != nil {
		if refresh {
			t.d.Cache.L2.Delete(l2Key)
		} else if blob, ok := t.d.Cache.L2.Get(l2Key); ok {
			t.d.logf("L2 hit for %s/extract_text", img.ID[:8])
			return renderOCR(string(blob), img.ID, provider.ModelVersion(), "L2")
		}
	}

	// OCR uses the hard profile: lossless PNG at up to 1500px with CLAHE, which
	// is exactly the enhancement that lifts accuracy on poorly lit document
	// photos.
	proc, err := t.d.preprocess(img, imgproc.HardOptions())
	if err != nil {
		return mcp.ErrorResult("Failed to preprocess image: " + err.Error())
	}

	resp, err := provider.Complete(ctx, vlm.Request{
		System:    extractSystemPrompt,
		Prompt:    extractPrompt(hint),
		Image:     proc.Data,
		ImageMIME: proc.MIMEType,
	})
	if err != nil {
		t.d.logf("extract_text failed: %v", err)
		return mcp.ErrorResult(fmt.Sprintf("OCR failed: %v. The image loaded correctly (image_id=%s), so this is a vision-provider problem; retry, or use measure_image for geometry in the meantime.", err, img.ID))
	}
	if t.d.Cache != nil {
		if err := t.d.Cache.L2.Put(l2Key, []byte(resp.Text)); err != nil {
			t.d.logf("warning: write L2: %v", err)
		}
	}
	return renderOCR(resp.Text, img.ID, resp.Model, "vlm")
}

func renderOCR(text, imageID, model, tier string) *mcp.ToolResult {
	trimmed := strings.TrimSpace(text)
	note := fmt.Sprintf("[image_id=%s; model=%s; cache=%s]", imageID, model, tier)

	if trimmed == "" || strings.EqualFold(trimmed, noTextSentinel) {
		return &mcp.ToolResult{Content: []map[string]any{
			mcp.TextContent("No readable text was found in this image."),
			mcp.TextContent(note),
		}}
	}
	return &mcp.ToolResult{Content: []map[string]any{
		mcp.TextContent(trimmed),
		mcp.TextContent(fmt.Sprintf("%d characters transcribed. %s", len([]rune(trimmed)), note)),
	}}
}
