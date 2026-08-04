// Command llm-eyes-mcp is an MCP stdio server that gives text-only LLMs a
// pair of eyes: geometric measurement, semantic description and OCR.
//
// Protocol invariant: stdout carries NDJSON JSON-RPC frames and nothing else.
// Every diagnostic goes to stderr. A single stray fmt.Println here would
// corrupt the session for the client, so this file never prints to stdout.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/xiaos/llm-eyes-mcp/internal/cache"
	"github.com/xiaos/llm-eyes-mcp/internal/config"
	"github.com/xiaos/llm-eyes-mcp/internal/imageio"
	"github.com/xiaos/llm-eyes-mcp/internal/mcp"
	"github.com/xiaos/llm-eyes-mcp/internal/tools"
	"github.com/xiaos/llm-eyes-mcp/internal/vlm"
)

const serverName = "llm-eyes-mcp"

// version is overridden at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	if code := run(os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

// run returns a process exit code instead of calling os.Exit so tests can
// drive it directly.
func run(args []string) int {
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "[%s] "+format+"\n", append([]any{serverName}, a...)...)
	}

	switch {
	case hasFlag(args, "--version", "-v"):
		fmt.Fprintf(os.Stderr, "%s %s\n", serverName, version)
		return 0
	case hasFlag(args, "--help", "-h"):
		usage()
		return 0
	}
	checkOnly := hasFlag(args, "--check")

	// ---- configuration -----------------------------------------------------
	path, found := config.ResolvePath(args)
	var cfg *config.Config
	if found {
		loaded, err := config.Load(path)
		if err != nil {
			logf("config error: %v", err)
			return 1
		}
		cfg = loaded
		logf("config loaded from %s", path)
	} else {
		logf("no config file found (checked --config, $%s, exe dir, cwd); "+
			"last candidate was %s", config.EnvConfigPath, path)
		logf("copy config.yml.example to %s and set your API key", path)
		return 1
	}

	if err := cfg.Validate(); err != nil {
		logf("config invalid: %v", err)
		return 1
	}

	// ---- VLM providers -----------------------------------------------------
	providers, buildErrs := vlm.BuildAll(cfg)
	for _, err := range buildErrs {
		logf("provider warning: %v", err)
	}
	if providers.Len() == 0 {
		logf("no usable provider: enable at least one entry under providers: in %s", path)
		return 1
	}
	logf("providers ready: %s (default %q)", strings.Join(providers.Names(), ", "), cfg.DefaultProvider)

	// ---- cache -------------------------------------------------------------
	root, err := cfg.ResolveCacheDir()
	if err != nil {
		logf("cache error: %v", err)
		return 1
	}
	mgr, err := cache.Open(cache.Settings{
		Root:         root,
		L1MaxBytes:   cfg.Cache.L1MaxBytes,
		L1TTL:        time.Duration(cfg.Cache.L1TTLHours) * time.Hour,
		L2MaxBytes:   cfg.Cache.L2MaxBytes,
		L2TTL:        time.Duration(cfg.Cache.L2TTLHours) * time.Hour,
		L3MaxEntries: cfg.Cache.L3MaxEntries,
	})
	if err != nil {
		logf("cache open failed: %v", err)
		return 1
	}
	defer func() {
		if err := mgr.Close(); err != nil {
			logf("cache close: %v", err)
		}
	}()

	// A startup sweep is best-effort: a corrupt cache entry must never stop the
	// server from serving.
	report, sweepErrs := mgr.Startup(cfg.CredentialFingerprint(), time.Duration(cfg.Cache.SweepIdleHours)*time.Hour)
	for _, err := range sweepErrs {
		logf("cache sweep warning: %v", err)
	}
	logf("cache ready at %s | %s", root, report)

	// ---- tools -------------------------------------------------------------
	loader := imageio.NewLoader(30 * time.Second)
	loader.Archive = mgr.ArchiveLookup()

	deps := &tools.Deps{
		Loader:    loader,
		Cache:     mgr,
		Providers: providers,
		MaxRounds: cfg.MaxRounds,
		Logf:      logf,
	}

	registry := mcp.NewRegistry()
	registry.Register(tools.NewMeasureTool(deps))
	registry.Register(tools.NewDescribeTool(deps))
	registry.Register(tools.NewExtractTool(deps))

	if checkOnly {
		names := make([]string, 0, registry.Len())
		for _, t := range registry.List() {
			names = append(names, t.Name())
		}
		logf("check ok: %d tools registered (%s)", registry.Len(), strings.Join(names, ", "))
		return 0
	}

	// ---- server ------------------------------------------------------------
	srv := mcp.NewServer(
		mcp.ServerInfo{Name: serverName, Version: version},
		registry,
		mcp.WithInstructions(tools.CapabilityBoundary),
	)

	// Ctrl-C / SIGTERM should still run the deferred cache close, so translate
	// the signal into a stdin close rather than an abrupt exit.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		logf("signal received, shutting down")
		_ = os.Stdin.Close()
	}()

	srv.Logf("%s %s ready on stdio", serverName, version)
	if err := srv.Start(); err != nil {
		logf("server stopped: %v", err)
		return 1
	}
	logf("stdin closed, exiting")
	return 0
}

func hasFlag(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n {
				return true
			}
		}
	}
	return false
}

func usage() {
	exe := serverName
	if p, err := os.Executable(); err == nil {
		exe = filepath.Base(p)
	}
	fmt.Fprintf(os.Stderr, `%s %s - MCP vision server for text-only LLMs

Usage:
  %s [--config <path>] [--check]

Flags:
  --config <path>  Path to config.yml (highest priority)
  --check          Validate config, build providers, then exit
  --version, -v    Print version
  --help, -h       Print this help

Config resolution order:
  1. --config <path>
  2. $%s
  3. config.yml next to the executable
  4. ./config.yml

Notes:
  stdout is reserved for the JSON-RPC NDJSON stream. All logs go to stderr.
`, serverName, version, exe, config.EnvConfigPath)
}
