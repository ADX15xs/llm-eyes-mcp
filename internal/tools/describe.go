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

// DescribeTool is the soft-pipeline entry point: qualitative perception that
// cannot be reduced to arithmetic.
type DescribeTool struct{ d *Deps }

// NewDescribeTool builds the description tool.
func NewDescribeTool(d *Deps) *DescribeTool { return &DescribeTool{d: d} }

func (t *DescribeTool) Name() string { return "describe_image" }

func (t *DescribeTool) Description() string {
	return "Analyse an image for semantic content, artistic style, colour harmony, emotional tone, and subject identification. " +
		"Use this ONLY when the user asks about aesthetics, scene understanding, or qualitative attributes. " +
		"Do NOT use it for measurements, distances, alignment or area (use measure_image), and do NOT use it to read text (use extract_text). " +
		"Returns prose, never numbers."
}

func (t *DescribeTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"image_source": map[string]any{
				"type": "string",
				"description": "The image to analyse. Accepts an http(s) URL, an absolute file path, a file:// URI, a data URI, raw base64, " +
					"or a 32-character image_id returned by a previous call (cheapest option: skips re-upload and hits the cache).",
			},
			"focus": map[string]any{
				"type":        "string",
				"enum":        []string{"style", "subject", "color", "emotion"},
				"description": "Optional: which aspect to analyse. style = medium, technique, composition. subject = what is depicted. color = palette and harmony. emotion = mood conveyed. Defaults to subject.",
			},
			"question": map[string]any{
				"type":        "string",
				"description": "Optional: a specific question to answer about the image, in addition to the chosen focus.",
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
		"required": []string{"image_source"},
	}
}

// Call runs the soft pipeline: aggressive downscale, then a single VLM call.
func (t *DescribeTool) Call(ctx context.Context, args map[string]any) *mcp.ToolResult {
	source := imageSourceArg(args)
	if source == "" {
		return mcp.ErrorResult("image_source is required: pass a URL, file path, data URI, base64 string, or a previously returned image_id.")
	}
	focus := strings.ToLower(getString(args, "focus"))
	if focus == "" {
		focus = "subject" // optional parameter -> server-side default, never empty passthrough
	}
	if _, ok := focusPrompts[focus]; !ok {
		return mcp.ErrorResult(fmt.Sprintf("Unsupported focus %q. Valid values: style, subject, color, emotion.", focus))
	}
	question := getString(args, "question")

	img, err := t.d.resolveImage(source)
	if err != nil {
		return mcp.ErrorResult("Failed to load image: " + err.Error())
	}
	provider, err := t.d.providerFor(args)
	if err != nil {
		return mcp.ErrorResult(err.Error())
	}
	refresh := getBool(args, "refresh")

	l2Key := cache.L2Key(img.ID, "describe_image", provider.ModelVersion(),
		cache.ParamHash(map[string]string{"focus": focus, "question": question}))

	if t.d.Cache != nil {
		if refresh {
			t.d.Cache.L2.Delete(l2Key)
		} else if blob, ok := t.d.Cache.L2.Get(l2Key); ok {
			t.d.logf("L2 hit for %s/describe_image", img.ID[:8])
			return renderText(string(blob), img.ID, focus, provider.ModelVersion(), "L2")
		}
	}

	// Soft pipeline: short edge 512px, JPEG q85. Low-frequency semantic content
	// survives this intact, and it cuts payload size by an order of magnitude.
	proc, err := t.d.preprocess(img, imgproc.SoftOptions())
	if err != nil {
		return mcp.ErrorResult("Failed to preprocess image: " + err.Error())
	}

	resp, err := provider.Complete(ctx, vlm.Request{
		System:    describeSystemPrompt,
		Prompt:    describePrompt(focus, question),
		Image:     proc.Data,
		ImageMIME: proc.MIMEType,
	})
	if err != nil {
		// Graceful degradation: the soft pipeline is the failure-prone one, so
		// point the agent at the capabilities that still work.
		t.d.logf("describe_image failed: %v", err)
		return mcp.ErrorResult(fmt.Sprintf(SoftDegradedMessage, err))
	}
	if t.d.Cache != nil {
		if err := t.d.Cache.L2.Put(l2Key, []byte(resp.Text)); err != nil {
			t.d.logf("warning: write L2: %v", err)
		}
	}
	return renderText(resp.Text, img.ID, focus, resp.Model, "vlm")
}

func renderText(text, imageID, focus, model, tier string) *mcp.ToolResult {
	note := fmt.Sprintf("[image_id=%s; focus=%s; model=%s; cache=%s]", imageID, focus, model, tier)
	return &mcp.ToolResult{Content: []map[string]any{
		mcp.TextContent(strings.TrimSpace(text)),
		mcp.TextContent(note),
	}}
}
