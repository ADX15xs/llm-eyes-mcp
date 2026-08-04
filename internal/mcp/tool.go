package mcp

import (
	"context"
	"encoding/base64"
	"sort"
)

// Tool is the narrow interface the protocol layer depends on. Business packages
// implement it; the protocol layer never imports them.
type Tool interface {
	// Name must be globally unique and stable across versions - renaming a tool
	// is a breaking change for every agent prompt and cache that references it.
	Name() string
	// Description is the agent's only basis for tool selection. Be precise about
	// when NOT to use the tool.
	Description() string
	// InputSchema is a JSON Schema object describing the arguments.
	InputSchema() map[string]any
	// Call executes the tool. Business failures must be returned as a
	// ToolResult with IsError=true, never as a Go error that aborts the request.
	Call(ctx context.Context, args map[string]any) *ToolResult
}

// ToolResult is the payload of a successful tools/call response. IsError=true
// still travels inside result (not JSON-RPC error) so the agent can read the
// reason and retry with different arguments.
type ToolResult struct {
	Content []map[string]any `json:"content"`
	IsError bool             `json:"isError"`
}

// TextResult builds a plain text result.
func TextResult(text string) *ToolResult {
	return &ToolResult{Content: []map[string]any{{"type": "text", "text": text}}}
}

// ErrorResult builds a business-failure result (isError=true).
func ErrorResult(text string) *ToolResult {
	return &ToolResult{
		Content: []map[string]any{{"type": "text", "text": text}},
		IsError: true,
	}
}

// TextContent builds a text content entry.
func TextContent(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

// ImageContent builds an image content entry. MCP requires data to be base64 -
// passing raw bytes through string() produces content no client can render.
func ImageContent(raw []byte, mimeType string) map[string]any {
	return map[string]any{
		"type":     "image",
		"data":     base64.StdEncoding.EncodeToString(raw),
		"mimeType": mimeType,
	}
}

// ResourceContent builds a resource reference entry.
func ResourceContent(uri, mimeType string) map[string]any {
	return map[string]any{
		"type": "resource",
		"resource": map[string]any{
			"uri":      uri,
			"mimeType": mimeType,
		},
	}
}

// Registry holds the tools exposed by the server. Writes happen during startup
// only; reads afterwards are concurrent-safe because the map is never mutated
// after Start.
type Registry struct {
	byName map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]Tool)}
}

// Register adds a tool, replacing any previous tool with the same name.
func (r *Registry) Register(t Tool) {
	r.byName[t.Name()] = t
}

// Get looks up a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// List returns tools sorted by name. Sorting is mandatory: Go map iteration is
// randomised, and an unstable tools/list order breaks client caches and tests.
func (r *Registry) List() []Tool {
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Tool, 0, len(names))
	for _, n := range names {
		out = append(out, r.byName[n])
	}
	return out
}

// Len reports the number of registered tools.
func (r *Registry) Len() int { return len(r.byName) }
