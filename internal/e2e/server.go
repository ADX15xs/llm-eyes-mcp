package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// ServerOptions configures SpawnServer.
type ServerOptions struct {
	Binary string    // path to the llm-eyes-mcp binary
	Args   []string  // command line for the server (e.g. --config config.yml)
	Env    []string  // extra KEY=VALUE entries appended to os.Environ()
	Stderr io.Writer // receives the server's stderr log; nil discards it
}

// SpawnServer starts the server binary and returns a Conn wired to its stdio.
// The returned stop function kills the process and reaps it; the harness owns
// the lifecycle, so it is called even on the success path (like the retired
// .mjs harness did).
func SpawnServer(ctx context.Context, opts ServerOptions) (*Conn, func(), error) {
	cmd := exec.CommandContext(ctx, opts.Binary, opts.Args...)
	cmd.Env = append(os.Environ(), opts.Env...)
	if opts.Stderr != nil {
		cmd.Stderr = opts.Stderr
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start %s (run make build first): %w", opts.Binary, err)
	}
	conn := NewConn(stdout, stdin)
	return conn, func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait() // "signal: killed" is the expected outcome here
	}, nil
}
