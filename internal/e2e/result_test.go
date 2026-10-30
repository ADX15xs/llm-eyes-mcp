package e2e

import (
	"encoding/json"
	"testing"

	"github.com/xiaos/llm-eyes-mcp/internal/mcp"
)

// frame builds a tools/call response whose result marshals from result.
func frame(t *testing.T, result any) *mcp.Response {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var resp mcp.Response
	if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":2,"result":`+string(raw)+`}`), &resp); err != nil {
		t.Fatal(err)
	}
	return &resp
}

func TestParseCompareResult(t *testing.T) {
	tests := []struct {
		name        string
		resp        *mcp.Response
		wantReport  CompareReport
		wantIsError bool
		wantErr     bool
	}{
		{
			name: "structured block parses",
			resp: frame(t, mcp.ToolResult{Content: []map[string]any{
				mcp.TextContent("compare_design score=1.00 (8/8 ok)"),
				mcp.TextContent("Structured result:\n" + `{"score":1,"regions_ok":8,"regions_total":8}`),
			}}),
			wantReport: CompareReport{Score: 1, OK: 8, Total: 8},
		},
		{
			name:        "business failure without structured block",
			resp:        frame(t, mcp.ErrorResult("image not found")),
			wantIsError: true,
			wantErr:     true,
		},
		{
			name:    "no structured block at all",
			resp:    frame(t, mcp.TextResult("hello")),
			wantErr: true,
		},
		{
			name: "malformed structured json",
			resp: frame(t, mcp.ToolResult{Content: []map[string]any{
				mcp.TextContent("Structured result:\n{oops"),
			}}),
			wantErr: true,
		},
		{
			name:        "json-rpc error frame",
			resp:        mustResponse(t, `{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"bad params"}}`),
			wantIsError: true,
			wantErr:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report, isErr, err := ParseCompareResult(tt.resp)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if isErr != tt.wantIsError {
				t.Errorf("isError = %v, want %v", isErr, tt.wantIsError)
			}
			if err == nil && report != tt.wantReport {
				t.Errorf("report = %+v, want %+v", report, tt.wantReport)
			}
		})
	}
}

func mustResponse(t *testing.T, raw string) *mcp.Response {
	t.Helper()
	var resp mcp.Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("bad test frame: %v", err)
	}
	return &resp
}
