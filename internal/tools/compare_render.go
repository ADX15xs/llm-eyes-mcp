package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xiaos/llm-eyes-mcp/internal/mcp"
)

func summarize(d compareDiff) string {
	total := len(d.regions)
	parts := make([]string, 0, len(d.regions))
	for _, r := range d.regions {
		if r.Status == "ok" {
			continue
		}
		if len(r.Issues) == 0 {
			parts = append(parts, fmt.Sprintf("%s: %s", r.ID, r.Status))
		} else {
			parts = append(parts, fmt.Sprintf("%s(%s)", r.ID, strings.Join(r.Issues, ",")))
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("All %d regions match", total)
	}
	pct := 0.0
	if total > 0 {
		pct = float64(d.ok) / float64(total) * 100
	}
	return fmt.Sprintf("%d/%d regions ok (%.0f%%). Issues: %s", d.ok, total, pct, strings.Join(parts, "; "))
}

func renderCompare(p *comparePayload) *mcp.ToolResult {
	notes := []string{
		fmt.Sprintf("image_id=%s (%dx%d px)", p.ImageID, p.ImageWidth, p.ImageHeight),
		"spec_hash=" + p.SpecHash[:min(8, len(p.SpecHash))],
		"cache=" + p.CacheTier,
	}
	if p.Model != "" {
		notes = append(notes, "model="+p.Model)
	}
	if p.Rounds > 0 {
		notes = append(notes, "vlm_rounds="+fmt.Sprintf("%d", p.Rounds))
	}
	headline := fmt.Sprintf("compare_design score=%.2f (%d/%d ok)\n%s", p.Score, p.OK, p.Total, p.Summary)
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
