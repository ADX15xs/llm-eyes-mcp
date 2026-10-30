// Command llm-eyes-e2e runs the matched-pair acceptance harness against the
// real gateway: it spawns the compiled server, speaks MCP over stdio, and
// asserts compare_design's structured report meets the score gate. It is the
// Go replacement for the retired scripts/compare_e2e.mjs.
//
// This is an integration harness, not a unit test: it needs UOMA_API_KEY
// (environment or repo-root .env) and network access to the provider. Never
// wire it into `make test` — Go unit tests never touch the network.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/xiaos/llm-eyes-mcp/internal/e2e"
	"github.com/xiaos/llm-eyes-mcp/internal/envfile"
	"github.com/xiaos/llm-eyes-mcp/internal/mcp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "llm-eyes-e2e:", err)
		os.Exit(1)
	}
}

func run() error {
	image := flag.String("image", "testdata/compare/yyhd_reward_section.png", "screenshot to compare")
	spec := flag.String("spec", "examples/yyhd_reward_spec.json", "spec JSON file")
	provider := flag.String("provider", "vision_glm", "VLM provider name from config.yml")
	cached := flag.Bool("cached", false, "allow a cached compare_design answer; default forces a fresh VLM run so the gate measures the live pipeline")
	minScore := flag.Float64("min-score", 1.0, "minimum structured score to pass")
	timeout := flag.Duration("timeout", 15*time.Minute, "overall deadline")
	serverBin := flag.String("server", defaultServerBinary(), "path to the llm-eyes-mcp binary")
	config := flag.String("config", "config.yml", "server config file")
	flag.Parse()

	apiKey := resolveAPIKey()
	if apiKey == "" {
		return errors.New("UOMA_API_KEY is not set (export it or add it to .env)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	conn, stop, err := e2e.SpawnServer(ctx, e2e.ServerOptions{
		Binary: *serverBin,
		Args:   []string{"--config", *config},
		Env:    []string{"UOMA_API_KEY=" + apiKey},
		Stderr: os.Stderr,
	})
	if err != nil {
		return err
	}
	defer stop()

	if _, err := conn.Call(ctx, "initialize", map[string]any{
		"protocolVersion": mcp.ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "llm-eyes-e2e", "version": "dev"},
	}); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if err := conn.Notify("notifications/initialized", nil); err != nil {
		return fmt.Errorf("initialized notify: %w", err)
	}

	resp, err := conn.Call(ctx, "tools/call", map[string]any{
		"name": "compare_design",
		"arguments": map[string]any{
			"image_source": *image,
			"spec":         *spec,
			"provider":     *provider,
			"refresh":      !*cached,
		},
	})
	if err != nil {
		return fmt.Errorf("tools/call: %w", err)
	}

	// Dump the full response for the run log before judging it.
	dump, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(dump))

	report, isErr, err := e2e.ParseCompareResult(resp)
	if err != nil {
		return err
	}
	if isErr {
		return errors.New("compare_design reported a business failure (isError=true)")
	}
	if report.Score < *minScore {
		return fmt.Errorf("score gate failed: got %.2f, need %.2f (%d/%d regions ok)",
			report.Score, *minScore, report.OK, report.Total)
	}
	fmt.Printf("PASS compare_design score=%.2f (%d/%d regions ok, min-score=%.2f)\n",
		report.Score, report.OK, report.Total, *minScore)
	return nil
}

// resolveAPIKey prefers the real environment, falling back to a repo-root .env
// so `make e2e` works without exporting anything first.
func resolveAPIKey() string {
	if key := os.Getenv("UOMA_API_KEY"); key != "" {
		return key
	}
	return envfile.Load(".env")["UOMA_API_KEY"]
}

// defaultServerBinary matches the Makefile's native build output name.
func defaultServerBinary() string {
	name := "bin/llm-eyes-mcp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}
