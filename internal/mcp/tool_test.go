package mcp

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// validContentBlock mirrors the MCP ContentBlock union
// (TextContent | ImageContent | AudioContent | ResourceLink | EmbeddedResource).
// Clients built on the official SDKs validate tool results against it and drop
// the whole response on a mismatch, so every block we emit must satisfy it.
func validContentBlock(b map[string]any) (bool, string) {
	str := func(k string) (string, bool) {
		v, ok := b[k].(string)
		return v, ok && v != ""
	}
	switch b["type"] {
	case "text":
		if _, ok := b["text"].(string); !ok {
			return false, "text block needs a string text field"
		}
	case "image", "audio":
		if _, ok := str("data"); !ok {
			return false, "image/audio block needs base64 data"
		}
		if _, ok := str("mimeType"); !ok {
			return false, "image/audio block needs mimeType"
		}
	case "resource_link":
		if _, ok := str("uri"); !ok {
			return false, "resource_link needs uri"
		}
		if _, ok := str("name"); !ok {
			return false, "resource_link needs name"
		}
	case "resource":
		res, ok := b["resource"].(map[string]any)
		if !ok {
			return false, "resource block needs a resource object"
		}
		if uri, ok := res["uri"].(string); !ok || uri == "" {
			return false, "embedded resource needs uri"
		}
		_, hasText := res["text"].(string)
		_, hasBlob := res["blob"].(string)
		if hasText == hasBlob {
			return false, "embedded resource needs exactly one of text or blob"
		}
	default:
		return false, "unknown content type"
	}
	return true, ""
}

func TestContentBuildersMatchSDKUnion(t *testing.T) {
	cases := map[string]map[string]any{
		"text":           TextContent("hello"),
		"image":          ImageContent([]byte{0x89, 0x50}, "image/png"),
		"text result":    TextResult("ok").Content[0],
		"error result":   ErrorResult("bad label").Content[0],
		"empty fallback": TextContent("No results."),
	}
	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			if ok, why := validContentBlock(block); !ok {
				t.Errorf("%s: %s: %+v", name, why, block)
			}
			if _, err := json.Marshal(block); err != nil {
				t.Errorf("%s is not JSON-serialisable: %v", name, err)
			}
		})
	}
}

func TestImageContentIsBase64(t *testing.T) {
	raw := []byte{0x00, 0xFF, 0x10, 0x89}
	block := ImageContent(raw, "image/png")

	data, ok := block["data"].(string)
	if !ok {
		t.Fatal("data is not a string")
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		t.Fatalf("data is not valid base64: %v", err)
	}
	if string(decoded) != string(raw) {
		t.Errorf("round-trip mismatch: got %v, want %v", decoded, raw)
	}
}

// A resource block carrying only {uri, mimeType} satisfies neither
// TextResourceContents nor BlobResourceContents and makes SDK clients reject
// the entire tool result. Guards against reintroducing such a builder.
func TestBareURIResourceBlockIsRejected(t *testing.T) {
	bad := map[string]any{
		"type": "resource",
		"resource": map[string]any{
			"uri":      "file:///tmp/a.png",
			"mimeType": "image/png",
		},
	}
	if ok, _ := validContentBlock(bad); ok {
		t.Error("a resource block without text or blob must be treated as invalid")
	}
}
