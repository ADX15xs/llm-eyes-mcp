// Package config loads, expands and validates the server configuration.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// EnvConfigPath is the environment variable that overrides the config path.
const EnvConfigPath = "LLM_EYES_CONFIG"

// Known provider adapter types.
const (
	TypeOpenAIVision = "openai_vision"
	TypeGeneric      = "generic"
)

// Config is the root configuration document.
type Config struct {
	// CacheDir is the root for L0/L1/L2 storage. Empty means ~/.llm-eyes-mcp.
	CacheDir string `yaml:"cache_dir"`
	// DefaultProvider names the provider used when a tool does not pick one.
	DefaultProvider string `yaml:"default_provider"`
	// MaxRounds caps VLM re-asks per tool call (convergence control).
	MaxRounds int `yaml:"max_rounds"`

	Cache     CacheConfig               `yaml:"cache"`
	Providers map[string]*ProviderConfig `yaml:"providers"`
}

// CacheConfig tunes the four cache tiers.
type CacheConfig struct {
	L1MaxBytes   int64 `yaml:"l1_max_bytes"`
	L1TTLHours   int   `yaml:"l1_ttl_hours"`
	L2MaxBytes   int64 `yaml:"l2_max_bytes"`
	L2TTLHours   int   `yaml:"l2_ttl_hours"`
	L3MaxEntries int   `yaml:"l3_max_entries"`
	// SweepIdleHours removes L1 entries untouched for this long at startup.
	SweepIdleHours int `yaml:"sweep_idle_hours"`
}

// ProviderConfig describes one VLM backend.
type ProviderConfig struct {
	Enabled bool   `yaml:"enabled"`
	Type    string `yaml:"type"`
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
	// ModelVersion participates in the L2 cache key so a model upgrade does not
	// silently serve stale answers.
	ModelVersion   string            `yaml:"model_version"`
	TimeoutSeconds int               `yaml:"timeout_seconds"`
	MaxTokens      int               `yaml:"max_tokens"`
	Temperature    *float64          `yaml:"temperature"`
	AuthMethod     string            `yaml:"auth_method"`
	CustomHeader   string            `yaml:"custom_header"`
	Headers        map[string]string `yaml:"headers"`
	// Extra carries backend-specific knobs without changing this struct.
	Extra map[string]any `yaml:"extra"`
}

// Timeout returns the configured HTTP timeout with a safe fallback. A zero
// http.Client timeout means "wait forever", which hangs the MCP client.
func (p *ProviderConfig) Timeout() time.Duration {
	if p.TimeoutSeconds > 0 {
		return time.Duration(p.TimeoutSeconds) * time.Second
	}
	return 60 * time.Second
}

// Default returns a configuration usable without any file on disk.
func Default() *Config {
	return &Config{
		// DefaultProvider is deliberately empty: Validate falls back to the
		// single enabled provider. Pre-filling a name here would reject any
		// config whose only provider happens to be called something else.
		DefaultProvider: "",
		MaxRounds:       2,
		Cache: CacheConfig{
			L1MaxBytes:     1 << 30, // 1 GiB
			L1TTLHours:     24,
			L2MaxBytes:     100 << 20, // 100 MiB
			L2TTLHours:     24 * 7,
			L3MaxEntries:   1000,
			SweepIdleHours: 48,
		},
		Providers: map[string]*ProviderConfig{},
	}
}

var envVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandEnvVars substitutes ${VAR} occurrences. Unset variables are left as-is
// so Validate can report them precisely instead of failing at request time.
func ExpandEnvVars(data []byte) []byte {
	return envVarRe.ReplaceAllFunc(data, func(match []byte) []byte {
		key := string(match[2 : len(match)-1])
		if val := os.Getenv(key); val != "" {
			return []byte(val)
		}
		return match
	})
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return Parse(raw)
}

// Parse builds a Config from YAML bytes.
func Parse(raw []byte) (*Config, error) {
	cfg := Default()
	if err := yaml.Unmarshal(ExpandEnvVars(raw), cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	d := Default()
	if c.MaxRounds <= 0 {
		c.MaxRounds = d.MaxRounds
	}
	if c.Cache.L1MaxBytes <= 0 {
		c.Cache.L1MaxBytes = d.Cache.L1MaxBytes
	}
	if c.Cache.L1TTLHours <= 0 {
		c.Cache.L1TTLHours = d.Cache.L1TTLHours
	}
	if c.Cache.L2MaxBytes <= 0 {
		c.Cache.L2MaxBytes = d.Cache.L2MaxBytes
	}
	if c.Cache.L2TTLHours <= 0 {
		c.Cache.L2TTLHours = d.Cache.L2TTLHours
	}
	if c.Cache.L3MaxEntries <= 0 {
		c.Cache.L3MaxEntries = d.Cache.L3MaxEntries
	}
	if c.Cache.SweepIdleHours <= 0 {
		c.Cache.SweepIdleHours = d.Cache.SweepIdleHours
	}
	for _, p := range c.Providers {
		if p == nil {
			continue
		}
		if p.Type == "" {
			p.Type = TypeOpenAIVision
		}
		if p.ModelVersion == "" {
			p.ModelVersion = p.Model
		}
		if p.AuthMethod == "" {
			p.AuthMethod = "bearer"
		}
	}
}

// EnabledNames returns enabled provider names in deterministic order.
func (c *Config) EnabledNames() []string {
	names := make([]string, 0, len(c.Providers))
	for n, p := range c.Providers {
		if p != nil && p.Enabled {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// Validate fails fast on misconfiguration. Every message names the offending
// config key so the user knows exactly what to edit.
func (c *Config) Validate() error {
	enabled := c.EnabledNames()
	if len(enabled) == 0 {
		return fmt.Errorf("providers: no provider is enabled; enable at least one in config.yml")
	}
	for _, name := range enabled {
		p := c.Providers[name]
		switch p.Type {
		case TypeOpenAIVision, TypeGeneric:
		default:
			return fmt.Errorf("providers.%s.type: unsupported type %q (want %q or %q)",
				name, p.Type, TypeOpenAIVision, TypeGeneric)
		}
		if p.BaseURL == "" {
			return fmt.Errorf("providers.%s.base_url: is required", name)
		}
		if hasUnexpanded(p.BaseURL) {
			return fmt.Errorf("providers.%s.base_url: unresolved environment variable %s", name, p.BaseURL)
		}
		if p.Model == "" {
			return fmt.Errorf("providers.%s.model: is required", name)
		}
		if hasUnexpanded(p.APIKey) {
			return fmt.Errorf("providers.%s.api_key: unresolved environment variable %s; export it or remove the placeholder",
				name, p.APIKey)
		}
		// Local deployments (llama.cpp, Ollama, vLLM) legitimately have no key.
		if p.APIKey == "" && !isLoopback(p.BaseURL) {
			return fmt.Errorf("providers.%s.api_key: is required for remote base_url %s", name, p.BaseURL)
		}
		if p.AuthMethod == "custom_header" && p.CustomHeader == "" {
			return fmt.Errorf("providers.%s.custom_header: is required when auth_method=custom_header", name)
		}
	}
	if c.DefaultProvider == "" {
		c.DefaultProvider = enabled[0]
	}
	if p, ok := c.Providers[c.DefaultProvider]; !ok || p == nil || !p.Enabled {
		return fmt.Errorf("default_provider: %q is not an enabled provider (enabled: %s)",
			c.DefaultProvider, strings.Join(enabled, ", "))
	}
	return nil
}

func hasUnexpanded(s string) bool { return envVarRe.MatchString(s) }

func isLoopback(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate()
	}
	return false
}

// CredentialFingerprint hashes all provider credentials. When it changes the
// L2 semantic cache is purged, because answers from a different account or
// endpoint must not be reused.
func (c *Config) CredentialFingerprint() string {
	h := sha256.New()
	for _, name := range c.EnabledNames() {
		p := c.Providers[name]
		fmt.Fprintf(h, "%s|%s|%s|%s\n", name, p.BaseURL, p.Model, p.APIKey)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ResolveCacheDir returns the effective cache root, creating it if needed.
func (c *Config) ResolveCacheDir() (string, error) {
	dir := c.CacheDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			dir = filepath.Join(os.TempDir(), "llm-eyes-mcp")
		} else {
			dir = filepath.Join(home, ".llm-eyes-mcp")
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir %s: %w", dir, err)
	}
	return dir, nil
}

// ResolvePath picks the config file location. MCP clients spawn the server with
// an unpredictable working directory, so a bare relative path is unreliable;
// four levels of fallback are checked in order.
//
// It returns the chosen path and whether that path actually exists.
func ResolvePath(args []string) (path string, found bool) {
	// 1. explicit --config flag. Its presence is authoritative: if the file is
	// missing we return it anyway so main() can report a clear "config error"
	// instead of silently falling through to other candidates.
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--config" || args[i] == "-config" {
			return args[i+1], true
		}
	}
	// 2. environment variable
	if p := os.Getenv(EnvConfigPath); p != "" {
		_, err := os.Stat(p)
		return p, err == nil
	}
	// 3. next to the executable
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "config.yml")
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	// 4. current working directory
	p := filepath.Join(".", "config.yml")
	_, err := os.Stat(p)
	return p, err == nil
}
