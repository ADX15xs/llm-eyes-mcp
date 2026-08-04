// Package vlm abstracts the vision-language backends. Providers self-register
// by type name; unknown types fall back to a tolerant OpenAI-compatible
// adapter, so adding a new backend is usually a config change, not code.
package vlm

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/xiaos/llm-eyes-mcp/internal/config"
)

// ProviderConfig aliases the config struct so implementations in this package
// do not have to spell out the import on every signature.
type ProviderConfig = config.ProviderConfig

// Request is one vision completion request.
type Request struct {
	System    string
	Prompt    string
	Image     []byte
	ImageMIME string
	MaxTokens int
	// Temperature overrides the provider default; nil means "use provider".
	Temperature *float64
	// JSONOnly asks the backend to emit strict JSON (used by the hard pipeline).
	JSONOnly bool
}

// Response is the backend's reply.
type Response struct {
	Text             string
	Model            string
	PromptTokens     int
	CompletionTokens int
}

// Provider is a vision backend.
type Provider interface {
	// Name is the configured provider key (e.g. "glm", "local-qwen").
	Name() string
	// ModelVersion feeds the L2 cache key so a model swap invalidates answers.
	ModelVersion() string
	// Complete performs one vision completion.
	Complete(ctx context.Context, req Request) (*Response, error)
}

// Builder constructs a provider from configuration.
type Builder func(name string, cfg *config.ProviderConfig) (Provider, error)

type registry struct {
	mu       sync.RWMutex
	builders map[string]Builder
}

var defaultRegistry = &registry{builders: map[string]Builder{}}

// Register makes a provider type available. Called from init() in each
// implementation file.
func Register(typeName string, b Builder) {
	defaultRegistry.mu.Lock()
	defer defaultRegistry.mu.Unlock()
	defaultRegistry.builders[typeName] = b
}

// RegisteredTypes lists known provider types in deterministic order.
func RegisteredTypes() []string {
	defaultRegistry.mu.RLock()
	defer defaultRegistry.mu.RUnlock()
	out := make([]string, 0, len(defaultRegistry.builders))
	for k := range defaultRegistry.builders {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Build constructs one provider, falling back to the generic OpenAI-compatible
// adapter when the type is unknown.
func Build(name string, cfg *config.ProviderConfig) (Provider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("providers.%s: nil configuration", name)
	}
	defaultRegistry.mu.RLock()
	b, ok := defaultRegistry.builders[cfg.Type]
	defaultRegistry.mu.RUnlock()
	if ok {
		return b(name, cfg)
	}
	return newOpenAIVision(name, cfg)
}

// Set is the collection of live providers.
type Set struct {
	byName       map[string]Provider
	defaultName  string
}

// BuildAll constructs every enabled provider. A single failure is collected and
// skipped rather than aborting startup: one broken backend should not take the
// whole server down.
func BuildAll(cfg *config.Config) (*Set, []error) {
	set := &Set{byName: map[string]Provider{}, defaultName: cfg.DefaultProvider}
	var errs []error
	for _, name := range cfg.EnabledNames() { // already sorted: deterministic
		p, err := Build(name, cfg.Providers[name])
		if err != nil {
			errs = append(errs, fmt.Errorf("providers.%s: %w", name, err))
			continue
		}
		set.byName[name] = p
	}
	if _, ok := set.byName[set.defaultName]; !ok {
		// The configured default failed to build; fall back to any survivor so
		// the server still works in degraded mode.
		for _, n := range set.Names() {
			set.defaultName = n
			break
		}
	}
	return set, errs
}

// NewSet builds a Set directly from providers (used by tests).
func NewSet(defaultName string, providers ...Provider) *Set {
	s := &Set{byName: map[string]Provider{}, defaultName: defaultName}
	for _, p := range providers {
		s.byName[p.Name()] = p
		if s.defaultName == "" {
			s.defaultName = p.Name()
		}
	}
	return s
}

// Names lists provider names in deterministic order.
func (s *Set) Names() []string {
	out := make([]string, 0, len(s.byName))
	for n := range s.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Len reports how many providers are live.
func (s *Set) Len() int { return len(s.byName) }

// Get returns a named provider, or the default when name is empty.
func (s *Set) Get(name string) (Provider, error) {
	if name == "" {
		name = s.defaultName
	}
	p, ok := s.byName[name]
	if !ok {
		return nil, fmt.Errorf("provider %q is not enabled (available: %v)", name, s.Names())
	}
	return p, nil
}

// Default returns the default provider.
func (s *Set) Default() (Provider, error) { return s.Get("") }
