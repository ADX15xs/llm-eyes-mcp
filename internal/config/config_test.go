package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// minimal is a valid single-provider document used as the base for mutations.
const minimal = `
providers:
  glm:
    enabled: true
    type: openai_vision
    base_url: https://open.bigmodel.cn/api/paas/v4
    api_key: sk-test
    model: glm-4v-flash
`

func mustParse(t *testing.T, raw string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

// ---------------------------------------------------------------------------
// defaults
// ---------------------------------------------------------------------------

func TestParseAppliesDefaults(t *testing.T) {
	cfg := mustParse(t, minimal)

	if cfg.MaxRounds != 2 {
		t.Errorf("MaxRounds = %d, want 2", cfg.MaxRounds)
	}
	if cfg.Cache.L1MaxBytes != 1<<30 {
		t.Errorf("L1MaxBytes = %d, want 1GiB", cfg.Cache.L1MaxBytes)
	}
	if cfg.Cache.L1TTLHours != 24 {
		t.Errorf("L1TTLHours = %d, want 24", cfg.Cache.L1TTLHours)
	}
	if cfg.Cache.L2MaxBytes != 100<<20 {
		t.Errorf("L2MaxBytes = %d, want 100MiB", cfg.Cache.L2MaxBytes)
	}
	if cfg.Cache.L2TTLHours != 24*7 {
		t.Errorf("L2TTLHours = %d, want 168", cfg.Cache.L2TTLHours)
	}
	if cfg.Cache.L3MaxEntries != 1000 {
		t.Errorf("L3MaxEntries = %d, want 1000", cfg.Cache.L3MaxEntries)
	}

	p := cfg.Providers["glm"]
	if p.AuthMethod != "bearer" {
		t.Errorf("auth_method = %q, want bearer (default)", p.AuthMethod)
	}
	// model_version defaults to model so the L2 key still moves on upgrade.
	if p.ModelVersion != "glm-4v-flash" {
		t.Errorf("model_version = %q, want it to default to model", p.ModelVersion)
	}
}

func TestProviderTypeDefaultsToOpenAIVision(t *testing.T) {
	cfg := mustParse(t, `
providers:
  x:
    enabled: true
    base_url: https://api.example.com/v1
    api_key: k
    model: m
`)
	if got := cfg.Providers["x"].Type; got != TypeOpenAIVision {
		t.Errorf("type = %q, want %q", got, TypeOpenAIVision)
	}
}

// Regression: a lone provider not called "glm" must be adopted as the default.
func TestDefaultProviderFallsBackToTheOnlyEnabledOne(t *testing.T) {
	cfg := mustParse(t, `
providers:
  ollama:
    enabled: true
    base_url: http://127.0.0.1:11434/v1
    model: qwen2.5vl
`)
	if cfg.DefaultProvider != "ollama" {
		t.Errorf("DefaultProvider = %q, want ollama", cfg.DefaultProvider)
	}
}

func TestExplicitDefaultProviderIsRespected(t *testing.T) {
	cfg := mustParse(t, `
default_provider: b
providers:
  a:
    enabled: true
    base_url: https://a.example.com/v1
    api_key: k
    model: m
  b:
    enabled: true
    base_url: https://b.example.com/v1
    api_key: k
    model: m
`)
	if cfg.DefaultProvider != "b" {
		t.Errorf("DefaultProvider = %q, want b", cfg.DefaultProvider)
	}
}

func TestProviderTimeoutFallback(t *testing.T) {
	p := &ProviderConfig{}
	if got := p.Timeout(); got != 60*time.Second {
		t.Errorf("zero timeout must not mean 'wait forever', got %v", got)
	}
	p.TimeoutSeconds = 5
	if got := p.Timeout(); got != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", got)
	}
}

func TestEnabledNamesIsSortedAndSkipsDisabled(t *testing.T) {
	cfg := mustParse(t, `
default_provider: alpha
providers:
  zeta:
    enabled: true
    base_url: https://z.example.com/v1
    api_key: k
    model: m
  alpha:
    enabled: true
    base_url: https://a.example.com/v1
    api_key: k
    model: m
  disabled:
    enabled: false
    base_url: https://d.example.com/v1
    model: m
`)
	got := cfg.EnabledNames()
	want := []string{"alpha", "zeta"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("EnabledNames = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// ${VAR} expansion
// ---------------------------------------------------------------------------

func TestExpandEnvVars(t *testing.T) {
	t.Setenv("LLM_EYES_TEST_KEY", "sk-secret")
	t.Setenv("LLM_EYES_TEST_URL", "https://env.example.com/v1")

	got := string(ExpandEnvVars([]byte("key: ${LLM_EYES_TEST_KEY}\nurl: ${LLM_EYES_TEST_URL}\n")))
	if !strings.Contains(got, "sk-secret") || !strings.Contains(got, "https://env.example.com/v1") {
		t.Errorf("expansion failed: %q", got)
	}
}

// Unset variables must survive verbatim so Validate can name them.
func TestExpandEnvVarsLeavesUnsetPlaceholders(t *testing.T) {
	got := string(ExpandEnvVars([]byte("key: ${DEFINITELY_NOT_SET_12345}")))
	if !strings.Contains(got, "${DEFINITELY_NOT_SET_12345}") {
		t.Errorf("unset placeholder was swallowed: %q", got)
	}
}

// The documented syntax is ${VAR} only. $VAR and ${VAR:-default} are NOT
// supported, and the README must not claim otherwise.
func TestExpandEnvVarsUnsupportedSyntaxIsLeftAlone(t *testing.T) {
	t.Setenv("LLM_EYES_TEST_KEY", "sk-secret")
	cases := []string{
		"$LLM_EYES_TEST_KEY",
		"${LLM_EYES_TEST_KEY:-fallback}",
		"$(LLM_EYES_TEST_KEY)",
		"%LLM_EYES_TEST_KEY%",
	}
	for _, in := range cases {
		if got := string(ExpandEnvVars([]byte(in))); got != in {
			t.Errorf("ExpandEnvVars(%q) = %q, want it untouched", in, got)
		}
	}
}

func TestParseExpandsAPIKeyFromEnv(t *testing.T) {
	t.Setenv("LLM_EYES_TEST_APIKEY", "sk-from-env")
	cfg := mustParse(t, `
providers:
  glm:
    enabled: true
    base_url: https://open.bigmodel.cn/api/paas/v4
    api_key: ${LLM_EYES_TEST_APIKEY}
    model: glm-4v-flash
`)
	if cfg.Providers["glm"].APIKey != "sk-from-env" {
		t.Errorf("api_key = %q", cfg.Providers["glm"].APIKey)
	}
}

// ---------------------------------------------------------------------------
// Validate - every message must name the offending key
// ---------------------------------------------------------------------------

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantKey string
	}{
		{
			"no providers at all",
			"providers: {}",
			"providers",
		},
		{
			"all providers disabled",
			"providers:\n  a:\n    enabled: false\n    base_url: https://a.example.com/v1\n    model: m\n",
			"providers",
		},
		{
			"unsupported type",
			"providers:\n  a:\n    enabled: true\n    type: gemini_native\n    base_url: https://a.example.com/v1\n    api_key: k\n    model: m\n",
			"providers.a.type",
		},
		{
			"missing base_url",
			"providers:\n  a:\n    enabled: true\n    api_key: k\n    model: m\n",
			"providers.a.base_url",
		},
		{
			"missing model",
			"providers:\n  a:\n    enabled: true\n    base_url: https://a.example.com/v1\n    api_key: k\n",
			"providers.a.model",
		},
		{
			"unresolved api_key placeholder",
			"providers:\n  a:\n    enabled: true\n    base_url: https://a.example.com/v1\n    api_key: ${NOPE_NOT_SET_98765}\n    model: m\n",
			"providers.a.api_key",
		},
		{
			"unresolved base_url placeholder",
			"providers:\n  a:\n    enabled: true\n    base_url: ${NOPE_URL_98765}\n    api_key: k\n    model: m\n",
			"providers.a.base_url",
		},
		{
			"remote provider without api_key",
			"providers:\n  a:\n    enabled: true\n    base_url: https://api.example.com/v1\n    model: m\n",
			"providers.a.api_key",
		},
		{
			"custom_header without header name",
			"providers:\n  a:\n    enabled: true\n    base_url: https://a.example.com/v1\n    api_key: k\n    model: m\n    auth_method: custom_header\n",
			"providers.a.custom_header",
		},
		{
			"default_provider points at a disabled provider",
			"default_provider: ghost\nproviders:\n  a:\n    enabled: true\n    base_url: https://a.example.com/v1\n    api_key: k\n    model: m\n",
			"default_provider",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.raw))
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Errorf("error %q does not name the offending key %q", err, tc.wantKey)
			}
		})
	}
}

// Local backends legitimately have no API key.
func TestValidateAllowsKeylessLoopbackProviders(t *testing.T) {
	for _, base := range []string{
		"http://localhost:11434/v1",
		"http://127.0.0.1:8214/v1",
		"http://0.0.0.0:8214/v1",
		"http://192.168.1.50:8000/v1",
	} {
		t.Run(base, func(t *testing.T) {
			_, err := Parse([]byte("providers:\n  local:\n    enabled: true\n    base_url: " + base + "\n    model: qwen2.5vl\n"))
			if err != nil {
				t.Errorf("local backend rejected: %v", err)
			}
		})
	}
}

func TestValidateRejectsGarbageYAML(t *testing.T) {
	if _, err := Parse([]byte("providers: [this is not a map")); err == nil {
		t.Error("want a parse error")
	}
}

// ---------------------------------------------------------------------------
// credential fingerprint
// ---------------------------------------------------------------------------

func TestCredentialFingerprintChangesWithTheKey(t *testing.T) {
	a := mustParse(t, minimal)
	b := mustParse(t, strings.Replace(minimal, "sk-test", "sk-different", 1))

	if a.CredentialFingerprint() == b.CredentialFingerprint() {
		t.Error("a changed api_key must change the fingerprint, otherwise L2 serves another account's answers")
	}
	if a.CredentialFingerprint() != mustParse(t, minimal).CredentialFingerprint() {
		t.Error("fingerprint is not stable across identical configs")
	}
}

func TestCredentialFingerprintChangesWithModelAndURL(t *testing.T) {
	base := mustParse(t, minimal).CredentialFingerprint()
	if mustParse(t, strings.Replace(minimal, "glm-4v-flash", "glm-4v-plus", 1)).CredentialFingerprint() == base {
		t.Error("model change must move the fingerprint")
	}
	if mustParse(t, strings.Replace(minimal, "open.bigmodel.cn", "other.host", 1)).CredentialFingerprint() == base {
		t.Error("base_url change must move the fingerprint")
	}
}

func TestCredentialFingerprintDoesNotLeakTheKey(t *testing.T) {
	fp := mustParse(t, minimal).CredentialFingerprint()
	if strings.Contains(fp, "sk-test") {
		t.Errorf("fingerprint leaks the raw key: %q", fp)
	}
	if len(fp) != 16 {
		t.Errorf("fingerprint length = %d, want 16", len(fp))
	}
}

// ---------------------------------------------------------------------------
// path resolution - the MCP client's cwd is unpredictable
// ---------------------------------------------------------------------------

func TestResolvePathPrefersExplicitFlag(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "custom.yml")
	if err := os.WriteFile(p, []byte(minimal), 0o600); err != nil {
		t.Fatal(err)
	}

	got, found := ResolvePath([]string{"--config", p})
	if !found || got != p {
		t.Errorf("ResolvePath = (%q, %v), want (%q, true)", got, found, p)
	}

	// The single-dash spelling works too.
	got, found = ResolvePath([]string{"-config", p})
	if !found || got != p {
		t.Errorf("-config: got (%q, %v)", got, found)
	}
}

func TestResolvePathReportsMissingFlagTarget(t *testing.T) {
	// An explicit --config is authoritative: ResolvePath returns it as found even
	// when the file is missing, so main() can report a clear "config error"
	// (via config.Load) instead of silently falling through to other candidates.
	missing := filepath.Join(t.TempDir(), "nope.yml")
	got, found := ResolvePath([]string{"--config", missing})
	if !found {
		t.Error("explicit --config must be authoritative even if the file is missing")
	}
	if got != missing {
		t.Errorf("path = %q, want %q", got, missing)
	}
	// And Load must fail loudly so the operator sees which path was bad.
	if _, err := Load(missing); err == nil {
		t.Error("Load should error for a missing --config target")
	}
}

func TestResolvePathUsesEnvVar(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "env.yml")
	if err := os.WriteFile(p, []byte(minimal), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvConfigPath, p)

	got, found := ResolvePath(nil)
	if !found || got != p {
		t.Errorf("ResolvePath = (%q, %v), want (%q, true)", got, found, p)
	}
}

func TestResolvePathFlagBeatsEnvVar(t *testing.T) {
	dir := t.TempDir()
	flagPath := filepath.Join(dir, "flag.yml")
	envPath := filepath.Join(dir, "env.yml")
	for _, p := range []string{flagPath, envPath} {
		if err := os.WriteFile(p, []byte(minimal), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(EnvConfigPath, envPath)

	got, _ := ResolvePath([]string{"--config", flagPath})
	if got != flagPath {
		t.Errorf("ResolvePath = %q, want the --config flag to win", got)
	}
}

func TestResolvePathFallsBackToCwd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(minimal), 0o600); err != nil {
		t.Fatal(err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	t.Setenv(EnvConfigPath, "")

	got, found := ResolvePath(nil)
	if !found {
		t.Fatalf("cwd fallback failed, got %q", got)
	}
	if filepath.Base(got) != "config.yml" {
		t.Errorf("path = %q", got)
	}
}

func TestResolvePathTrailingFlagWithoutValue(t *testing.T) {
	t.Setenv(EnvConfigPath, "")
	// "--config" as the last argument must not panic on the missing value.
	_, _ = ResolvePath([]string{"--config"})
}

// ---------------------------------------------------------------------------
// Load / cache dir
// ---------------------------------------------------------------------------

func TestLoadFromDisk(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(p, []byte(minimal), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Providers["glm"].Model != "glm-4v-flash" {
		t.Errorf("model = %q", cfg.Providers["glm"].Model)
	}
}

func TestLoadMissingFileNamesThePath(t *testing.T) {
	p := filepath.Join(t.TempDir(), "absent.yml")
	_, err := Load(p)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "absent.yml") {
		t.Errorf("error %q does not name the missing path", err)
	}
}

func TestResolveCacheDirCreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "cache")
	cfg := Default()
	cfg.CacheDir = dir

	got, err := cfg.ResolveCacheDir()
	if err != nil {
		t.Fatalf("ResolveCacheDir: %v", err)
	}
	if got != dir {
		t.Errorf("dir = %q, want %q", got, dir)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Errorf("directory was not created: %v", err)
	}
}
