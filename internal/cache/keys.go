// Package cache implements the four-tier cache that makes repeated questions
// about the same image nearly free.
//
//	L0 raw:  original bytes, never transformed        (disk, no TTL)
//	L1 proc: pipeline output, per intent              (disk, 24h, 1GiB LRU)
//	L2 vlm:  VLM JSON/text, per tool+model            (SQLite, 7d, 100MiB)
//	L3 geo:  computed floats, per action+params       (memory LRU, session)
//
// Every key is derived from the image content MD5 - never a filename or URL,
// because two different images with the same name would otherwise serve each
// other's answers.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// SchemaVersion invalidates every derived tier when the processing or prompt
// contract changes. Bump it in the same commit as the behaviour change.
const SchemaVersion = "v1"

// L0Key addresses the untouched original bytes.
func L0Key(md5 string) string { return "raw:" + strings.ToLower(md5) }

// L1Key addresses a preprocessed variant. The intent tag keeps hard and soft
// renditions of the same source image in separate slots.
func L1Key(md5, intentTag string) string {
	return fmt.Sprintf("proc:%s_%s_%s", strings.ToLower(md5), intentTag, SchemaVersion)
}

// L2Key addresses a VLM response. It is keyed by tool name rather than by the
// user's phrasing, so "how far apart" and "what is the distance" hit the same
// entry. The model version is included because two model builds can disagree
// about the same picture.
func L2Key(md5, toolName, modelVersion, paramHash string) string {
	return fmt.Sprintf("vlm:%s_%s_%s_%s_%s",
		strings.ToLower(md5), toolName, sanitize(modelVersion), paramHash, SchemaVersion)
}

// L3Key addresses a computed geometry value.
func L3Key(md5, action string, params map[string]string) string {
	return fmt.Sprintf("geo:%s_%s_%s_%s",
		strings.ToLower(md5), action, sortedParams(params), SchemaVersion)
}

// ParamHash produces a short, order-independent digest of tool parameters.
func ParamHash(params map[string]string) string {
	if len(params) == 0 {
		return "noparam"
	}
	sum := sha256.Sum256([]byte(sortedParams(params)))
	return hex.EncodeToString(sum[:])[:12]
}

func sortedParams(params map[string]string) string {
	if len(params) == 0 {
		return ""
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params[k])
	}
	return strings.Join(parts, "&")
}

// sanitize makes a value safe to embed in a key and a filename.
func sanitize(s string) string {
	if s == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			return r
		default:
			return '_'
		}
	}, s)
}

// fileNameFor turns an arbitrary cache key into a collision-free filename.
func fileNameFor(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
