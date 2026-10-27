package tools

import (
	"fmt"
	"strings"
)

// CapabilityBoundary is returned to the client in the initialize response. It
// states up front what this server is and is not for, so agents stop routing
// aesthetic questions into the measurement path.
const CapabilityBoundary = `llm-eyes-mcp is a geometric measurement and structural layout verification instrument, not an image critic.

What it does well:
  - Precise spatial measurement on rigid, man-made subjects (PCBs, mechanical parts, floor plans, UI screenshots, shelves).
  - Layout compliance checks: centering, alignment, overlap (IoU), bounding-box area.
  - Text and table extraction from dense documents, receipts and reports.
  - Design-revert comparison: given a structured design spec (JSON file) plus a CROPPED screenshot of ONE component, reports per-region colour delta, position IoU, and text match in pixels.

What it cannot do (do not route these here):
  - Subjective aesthetics or emotional judgement - there is no mathematical definition of "melancholy" in a coordinate system.
  - Instance segmentation in cluttered natural scenes with no clear object boundaries.
  - 3D perspective or depth reasoning - a single 2D photo cannot be converted to Euclidean world distances.

Accuracy contract: all measurements are reported in PIXELS of the original image. Absolute physical units (mm, inches) are NOT available without a calibration target. Coordinate quality is bounded by the vision model; typical normalised bounding-box error is 1-3%.

Tool selection:
  - measure_image  -> distance, alignment, area, overlap. Returns numbers.
  - describe_image -> style, subject, colour, emotion. Returns prose.
  - extract_text   -> OCR. Returns text.
  - compare_design -> structured design spec + cropped screenshot -> per-region fidelity report (score, bbox IoU, colour delta, text equality). For D2C design-revert work. See docs/spec-schema.md for spec contract.

Pass image_source once; reuse the returned image_id on later turns to hit the cache and skip re-uploading.`

// measureSystemPrompt constrains the VLM to the one job it is good at:
// reporting coordinates. Every derived number is computed in Go afterwards.
const measureSystemPrompt = `You are a precision coordinate annotator for a measurement instrument. Your ONLY job is to locate objects and report their bounding boxes.

Rules:
1. Reply with RAW JSON only. No markdown fences, no prose, no explanation.
2. Schema: {"objects":[{"label":"<requested label>","bbox":[x0,y0,x1,y1],"confidence":0.0-1.0}]}
3. Coordinates are NORMALISED floats in [0,1]: x0,y0 = top-left corner, x1,y1 = bottom-right corner. Origin is the top-left of the image.
4. Use EXACTLY the label strings the user requested, one entry per requested label.
5. If a requested object is genuinely absent, still emit an entry with "confidence": 0 and bbox [0,0,0,0]. Never omit a label, never invent extra objects.
6. Do not estimate distances, sizes or relationships. Coordinates only.`

// measurePrompt is the first-round user prompt.
func measurePrompt(labels []string, measureType string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Locate the following %d object(s) in this image and return their bounding boxes as JSON:\n", len(labels))
	for _, l := range labels {
		fmt.Fprintf(&b, "  - %q\n", l)
	}
	fmt.Fprintf(&b, "\nThe downstream system will compute %s from your coordinates, so tight and accurate boxes matter more than anything else.\n", measureType)
	b.WriteString(`Return only: {"objects":[{"label":"...","bbox":[x0,y0,x1,y1],"confidence":0.9}]}`)
	return b.String()
}

// measureRetryPrompt is the second (and final) round. Convergence is capped at
// two rounds so a stubborn model cannot burn unbounded tokens.
func measureRetryPrompt(missing []string, previous string) string {
	var b strings.Builder
	b.WriteString("Your previous reply was unusable. ")
	if len(missing) > 0 {
		b.WriteString("These requested labels were missing or malformed: ")
		for i, m := range missing {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", m)
		}
		b.WriteString(".\n")
	}
	if previous != "" {
		fmt.Fprintf(&b, "You replied: %s\n", truncate(previous, 300))
	}
	b.WriteString(`Reply again with RAW JSON only, no markdown fences, exactly this shape and one entry per requested label: {"objects":[{"label":"...","bbox":[x0,y0,x1,y1],"confidence":0.9}]}`)
	return b.String()
}

// describeSystemPrompt drives the soft pipeline.
const describeSystemPrompt = `You are a visual analyst. Describe what you observe accurately and concisely.
Rules:
1. Be specific and grounded in what is actually visible; never invent details.
2. Do not output coordinates, pixel measurements or bounding boxes - a separate instrument handles those.
3. Keep the answer under 200 words unless the image genuinely requires more.
4. Plain prose. No markdown headings.`

var focusPrompts = map[string]string{
	"style":   "Analyse the artistic and visual style of this image: medium, technique, composition, era or genre influences, and overall visual treatment.",
	"subject": "Identify the main subject and scene of this image: what is depicted, what the objects are, what is happening, and the setting.",
	"color":   "Analyse the colour of this image: dominant palette, colour harmony or clashes, saturation and temperature, and how colour directs attention.",
	"emotion": "Analyse the emotional tone and mood of this image: the feeling it conveys and the visual cues that produce it.",
}

// describePrompt builds the soft-pipeline user prompt.
func describePrompt(focus, extra string) string {
	base, ok := focusPrompts[focus]
	if !ok {
		base = focusPrompts["subject"]
	}
	if extra != "" {
		return base + "\n\nAlso address this specific question: " + extra
	}
	return base
}

// extractSystemPrompt drives OCR.
const extractSystemPrompt = `You are an OCR engine. Transcribe text from images exactly as written.
Rules:
1. Output ONLY the transcribed text. No commentary, no "Here is the text:" preamble.
2. Preserve the original reading order and line breaks.
3. Render tables as markdown tables, preserving row and column structure.
4. Transcribe digits, punctuation and units character-for-character. Never normalise, correct or complete them.
5. For text you genuinely cannot read, write [illegible]. Never guess.
6. If the image contains no text at all, reply with exactly: NO_TEXT_FOUND`

// extractPrompt builds the OCR user prompt.
func extractPrompt(extra string) string {
	base := "Transcribe all human-readable text in this image, preserving reading order and table structure."
	if extra != "" {
		return base + "\n\nFocus in particular on: " + extra
	}
	return base
}

// SoftDegradedMessage is returned when the soft pipeline fails, pointing the
// agent at the capability that still works instead of dead-ending.
const SoftDegradedMessage = "Semantic analysis is unavailable right now (%v). Geometric capabilities are unaffected: use measure_image for distance, alignment or area, or extract_text for OCR."

// compareSystemPrompt drives the compare_design locator: VLM only reports
// boxes (with text transcription) — Go computes all deltas.
const compareSystemPrompt = `You are a region locator for a design-revert verification instrument. Your ONLY job is to locate regions described in the input and report their bounding boxes plus any text you see inside them.

Rules:
1. Reply with RAW JSON only. No markdown fences, no prose, no explanation.
2. Reply in exactly the shape the user message requests: one region → a single object {"id":"...","bbox":[x0,y0,x1,y1],"text":"..."}; several regions → {"regions":[...one object per region...]}.
3. Coordinates are NORMALISED floats in [0,1]: x0,y0 = top-left corner, x1,y1 = bottom-right corner. Origin is the top-left of the image.
4. Use EXACTLY the id strings the spec gives you, one entry per region. Never invent extra regions, never merge two.
5. The downstream system will compare your boxes against the spec and compute colour/text deltas in code. Accurate boxes matter more than anything else.
6. If a region is genuinely absent from this screenshot, still emit its entry with bbox [0,0,0,0] and text "".
7. Do not estimate distances, sizes or colours. Boxes and text only.`

// compareLocatePrompt builds the first-round prompt from a spec. A
// single-region batch drops the multi-region envelope: the model replies with
// one bare object, the shape verified reliable against the current upstream
// (multi-region prompts exhaust the token budget on reasoning and return NULL).
func compareLocatePrompt(spec *compareSpec) string {
	if len(spec.Regions) == 1 {
		return compareLocateSinglePrompt(spec.Regions[0])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Locate the following %d region(s) in this screenshot and return their bounding boxes plus any text inside. Return raw JSON only:\n", len(spec.Regions))
	for _, r := range spec.Regions {
		b.WriteString("  - id=")
		b.WriteString(r.ID)
		if r.Desc != "" {
			b.WriteString(" desc=" + r.Desc)
		}
		if r.Color != "" {
			b.WriteString(" color=" + r.Color)
		}
		if r.Text != "" {
			b.WriteString(" text=" + r.Text)
		}
		b.WriteString("\n")
	}
	b.WriteString(`Return only: {"regions":[{"id":"...","bbox":[x0,y0,x1,y1],"text":"..."}]}`)
	return b.String()
}

// compareLocateSinglePrompt builds the minimal one-region prompt. It keeps the
// "- id=" bullet format so replies stay parseable by the same machinery.
func compareLocateSinglePrompt(r compareRegion) string {
	var b strings.Builder
	b.WriteString("Locate this one region in the screenshot and return its bounding box plus any text inside. Return raw JSON only:\n")
	b.WriteString("  - id=")
	b.WriteString(r.ID)
	if r.Desc != "" {
		b.WriteString(" desc=" + r.Desc)
	}
	if r.Color != "" {
		b.WriteString(" color=" + r.Color)
	}
	if r.Text != "" {
		b.WriteString(" text=" + r.Text)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, `Return only: {"id":%q,"bbox":[x0,y0,x1,y1],"text":"..."}. Coordinates are NORMALISED floats in [0,1], origin top-left. If the region is absent, still reply with bbox [0,0,0,0] and text "".`, r.ID)
	return b.String()
}

// compareLocateRetryPrompt is the second (and final) round. Convergence is capped
// at max_rounds so a stubborn model cannot burn unbounded tokens. It requests
// the same reply shape as the first round for the given batch.
func compareLocateRetryPrompt(spec *compareSpec, missing []string, previous string) string {
	var b strings.Builder
	b.WriteString("Your previous reply was unusable. ")
	if len(missing) > 0 {
		b.WriteString("These region ids were missing or malformed: ")
		for i, m := range missing {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(m)
		}
		b.WriteString(".\n")
	}
	if previous != "" {
		fmt.Fprintf(&b, "You replied: %s\n", truncate(previous, 300))
	}
	if len(spec.Regions) == 1 {
		b.WriteString(`Reply again with RAW JSON only, no markdown fences, exactly one object: {"id":"...","bbox":[x0,y0,x1,y1],"text":"..."}`)
		return b.String()
	}
	b.WriteString(`Reply again with RAW JSON only, no markdown fences, exactly one entry per region id: {"regions":[{"id":"...","bbox":[x0,y0,x1,y1],"text":"..."}]}`)
	return b.String()
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "...[truncated]"
}
