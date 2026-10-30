package e2e

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xiaos/llm-eyes-mcp/internal/mcp"
)

// StructuredMarker prefixes the content block carrying compare_design's
// machine-readable report. It must stay in sync with
// internal/tools/compare_render.go, which is where the block is produced.
const StructuredMarker = "Structured result:"

// CompareReport is the slice of compare_design's structured result the
// acceptance gate asserts on. Field names mirror the server's comparePayload
// JSON tags.
type CompareReport struct {
	Score float64 `json:"score"`
	OK    int     `json:"regions_ok"`
	Total int     `json:"regions_total"`
}

// ParseCompareResult extracts the structured report and the business-failure
// flag from a tools/call response. A JSON-RPC error frame or a missing/malformed
// structured block is a harness error, not an acceptance verdict.
func ParseCompareResult(resp *mcp.Response) (report CompareReport, isError bool, err error) {
	if resp.Error != nil {
		return report, true, fmt.Errorf("json-rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return report, false, fmt.Errorf("re-encode tools/call result: %w", err)
	}
	var result mcp.ToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return report, false, fmt.Errorf("decode tools/call result: %w", err)
	}
	for _, entry := range result.Content {
		text, _ := entry["text"].(string)
		idx := strings.Index(text, StructuredMarker)
		if idx < 0 {
			continue
		}
		blob := strings.TrimSpace(text[idx+len(StructuredMarker):])
		if err := json.Unmarshal([]byte(blob), &report); err != nil {
			return CompareReport{}, result.IsError, fmt.Errorf("structured result: %w", err)
		}
		return report, result.IsError, nil
	}
	return CompareReport{}, result.IsError, fmt.Errorf("no %q content block in result", StructuredMarker)
}
