// Package tools implements the three focused MCP tools. Intent routing is done
// by the agent's tool selection, so each tool here only performs deterministic
// execution - it never tries to guess what the user "really meant".
package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xiaos/llm-eyes-mcp/internal/cache"
	"github.com/xiaos/llm-eyes-mcp/internal/imageio"
	"github.com/xiaos/llm-eyes-mcp/internal/imgproc"
	"github.com/xiaos/llm-eyes-mcp/internal/vlm"
)

// Deps carries everything the tools need. Injected so tests can swap the VLM
// for a mock and the cache for a temp directory.
type Deps struct {
	Loader    *imageio.Loader
	Cache     *cache.Manager
	Providers *vlm.Set
	// MaxRounds caps VLM re-asks per call (convergence control, default 2).
	MaxRounds int
	// Logf writes diagnostics to stderr. Never stdout.
	Logf func(format string, a ...any)
}

func (d *Deps) logf(format string, a ...any) {
	if d.Logf != nil {
		d.Logf(format, a...)
	}
}

func (d *Deps) maxRounds() int {
	if d.MaxRounds <= 0 {
		return 2
	}
	return d.MaxRounds
}

// resolveImage loads an image from any supported source and archives the
// original bytes in L0 so later turns can refer to it by image_id alone.
func (d *Deps) resolveImage(source string) (*imageio.Image, error) {
	img, err := d.Loader.Load(source)
	if err != nil {
		return nil, err
	}
	if d.Cache != nil && !d.Cache.L0.Has(img.ID) {
		if err := d.Cache.L0.Put(img.ID, img.Data); err != nil {
			// A failed archive write is not fatal: the request can still run,
			// we just lose the image_id shortcut for later turns.
			d.logf("warning: archive image %s: %v", img.ID, err)
		}
	}
	return img, nil
}

// preprocess runs the pipeline, serving from L1 when a matching rendition was
// already produced for this exact image and intent.
func (d *Deps) preprocess(img *imageio.Image, opts imgproc.Options) (*imgproc.Output, error) {
	key := cache.L1Key(img.ID, opts.CacheTag())
	if d.Cache != nil {
		if data, ok := d.Cache.L1.Get(key); ok {
			mime := "image/png"
			if opts.Pipeline == imgproc.PipelineSoft {
				mime = "image/jpeg"
			}
			d.logf("L1 hit %s (%d bytes)", opts.CacheTag(), len(data))
			return &imgproc.Output{
				Data:         data,
				MIMEType:     mime,
				SourceWidth:  img.Width,
				SourceHeight: img.Height,
				Pipeline:     opts.Pipeline,
			}, nil
		}
	}
	out, err := imgproc.Process(img.Data, opts)
	if err != nil {
		return nil, err
	}
	if d.Cache != nil {
		if err := d.Cache.L1.Put(key, out.Data); err != nil {
			d.logf("warning: write L1 %s: %v", key, err)
		}
	}
	d.logf("preprocessed %s via %s pipeline: %dx%d -> %dx%d, %d bytes",
		img.ID[:8], opts.Pipeline, out.SourceWidth, out.SourceHeight, out.Width, out.Height, len(out.Data))
	return out, nil
}

// ---------------------------------------------------------------------------
// Defensive argument helpers.
//
// After JSON decoding into map[string]any every number is a float64 and every
// field may be missing or the wrong type. Direct type assertions would panic on
// input we do not control, so all access goes through these.
// ---------------------------------------------------------------------------

func getString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	switch s := v.(type) {
	case string:
		return strings.TrimSpace(s)
	case fmt.Stringer:
		return strings.TrimSpace(s.String())
	}
	return ""
}

func getBool(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok {
		return false
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return strings.EqualFold(b, "true") || b == "1"
	case float64:
		return b != 0
	}
	return false
}

// getStringSlice accepts a JSON array of strings, or a single comma-separated
// string. Agents produce both shapes in practice.
func getStringSlice(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	var out []string
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
	case []string:
		for _, s := range t {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, s := range strings.Split(t, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// imageSourceArg reads the image argument, accepting the documented name plus
// the aliases agents commonly invent.
func imageSourceArg(args map[string]any) string {
	for _, k := range []string{"image_source", "image_uri", "image", "image_id", "image_url", "path"} {
		if v := getString(args, k); v != "" {
			return v
		}
	}
	return ""
}

// providerFor resolves the provider named in the args, or the default.
func (d *Deps) providerFor(args map[string]any) (vlm.Provider, error) {
	return d.Providers.Get(getString(args, "provider"))
}

// paramKey builds a deterministic parameter map for cache keys.
func paramKey(pairs map[string]string) map[string]string {
	out := make(map[string]string, len(pairs))
	for k, v := range pairs {
		out[k] = v
	}
	return out
}

// sortedCopy returns a sorted copy, used so ["b","a"] and ["a","b"] share one
// cache entry.
func sortedCopy(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}
