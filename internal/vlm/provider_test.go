package vlm

import (
	"context"
	"testing"

	"github.com/xiaos/llm-eyes-mcp/internal/config"
)

// provider_test covers the registry, multi-provider build, generic fallback,
// and the Set collection used at request time.

// fakeProvider is a trivial Provider used to verify the registry/build path.
type fakeProvider struct {
	name  string
	model string
	calls int
}

func (f *fakeProvider) Name() string         { return f.name }
func (f *fakeProvider) ModelVersion() string { return f.model }
func (f *fakeProvider) Complete(context.Context, Request) (*Response, error) {
	f.calls++
	return &Response{Text: "from-" + f.name, Model: f.model}, nil
}

func TestRegisterAndBuild(t *testing.T) {
	Register("test_double", func(name string, cfg *config.ProviderConfig) (Provider, error) {
		return &fakeProvider{name: name, model: cfg.Model}, nil
	})
	p, err := Build("myname", &config.ProviderConfig{Type: "test_double", Model: "mm"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Name() != "myname" {
		t.Errorf("built provider name = %q, want myname", p.Name())
	}
	if p.ModelVersion() != "mm" {
		t.Errorf("built provider model = %q, want mm", p.ModelVersion())
	}
}

func TestBuildFallsBackToGenericOnUnknownType(t *testing.T) {
	// An unregistered type must fall back to the OpenAI-compatible adapter
	// rather than failing, so adding a backend is usually a config change.
	p, err := Build("x", &config.ProviderConfig{
		Type:    "mystery_backend",
		BaseURL: "http://example/v1",
		Model:   "m-model",
	})
	if err != nil {
		t.Fatalf("Build must fall back to generic, got error: %v", err)
	}
	if p.Name() != "x" {
		t.Errorf("provider name = %q, want x", p.Name())
	}
	if p.ModelVersion() != "m-model" {
		t.Errorf("provider model version = %q, want m-model", p.ModelVersion())
	}
}

func TestBuildRejectsMissingBaseURL(t *testing.T) {
	_, err := Build("x", &config.ProviderConfig{Type: "openai_vision", Model: "m"})
	if err == nil {
		t.Errorf("Build must require a base_url")
	}
}

func TestBuildAllCollectsEnabledProviders(t *testing.T) {
	cfg := &config.Config{
		DefaultProvider: "glm",
		Providers: map[string]*config.ProviderConfig{
			"glm":   {Enabled: true, Type: "openai_vision", BaseURL: "http://glm/v1", Model: "glm-4v"},
			"local": {Enabled: true, Type: "openai_vision", BaseURL: "http://local/v1", Model: "qwen-vl"},
			"off":   {Enabled: false, Type: "openai_vision", BaseURL: "http://off/v1", Model: "x"},
		},
	}
	set, errs := BuildAll(cfg)
	if len(errs) != 0 {
		t.Fatalf("BuildAll errors: %v", errs)
	}
	if set.Len() != 2 {
		t.Fatalf("BuildAll should build only enabled providers: len=%d", set.Len())
	}
	names := set.Names()
	if len(names) != 2 || names[0] != "glm" || names[1] != "local" {
		t.Errorf("Names must be sorted enabled names: %v", names)
	}
}

func TestBuildAllFallsBackWhenDefaultFails(t *testing.T) {
	// The configured default is broken (no base_url); BuildAll must skip it and
	// re-home the default to a survivor instead of aborting.
	cfg := &config.Config{
		DefaultProvider: "broken",
		Providers: map[string]*config.ProviderConfig{
			"broken": {Enabled: true, Type: "openai_vision", Model: "x"}, // missing base_url
			"good":   {Enabled: true, Type: "openai_vision", BaseURL: "http://good/v1", Model: "m"},
		},
	}
	set, errs := BuildAll(cfg)
	if len(errs) == 0 {
		t.Errorf("expected a collected error for the broken default")
	}
	if set.Len() != 1 {
		t.Fatalf("survivor must remain: len=%d", set.Len())
	}
	def, err := set.Default()
	if err != nil {
		t.Fatalf("Default must resolve to the survivor: %v", err)
	}
	if def.Name() != "good" {
		t.Errorf("default should fall back to good, got %q", def.Name())
	}
}

func TestSetGetDefaultAndMissing(t *testing.T) {
	glm := &fakeProvider{name: "glm", model: "glm-4v"}
	local := &fakeProvider{name: "local", model: "qwen-vl"}
	set := NewSet("glm", glm, local)

	if p, err := set.Get(""); err != nil || p.Name() != "glm" {
		t.Errorf("Get(\"\") must return the default glm: %v", err)
	}
	if p, err := set.Get("local"); err != nil || p.Name() != "local" {
		t.Errorf("Get(local) must return local: %v", err)
	}
	if _, err := set.Get("nope"); err == nil {
		t.Errorf("Get(missing) must error")
	}
	if set.Len() != 2 {
		t.Errorf("Len must be 2, got %d", set.Len())
	}
}
