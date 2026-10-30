# AGENTS.md — for AI agents working on llm-eyes-mcp

This file tells an agent (or a future you) how to navigate, extend, and test
this codebase without breaking its invariants.

## What this is

An MCP **stdio** server (`cmd/llm-eyes-mcp`) that gives a text-only LLM vision
via four tools: `measure_image`, `describe_image`, `extract_text`,
`compare_design`. It is **zero-CGO** and ships as a single static binary
(<20 MB).

## Hard invariants (do not violate)

1. **stdout is the JSON-RPC stream.** Never write to stdout except NDJSON
   frames. All logging goes to stderr. A stray `fmt.Println` corrupts the
   client session. The protocol layer (`internal/mcp`) owns stdout; business
   code must not touch it.
2. **Business failures are `ToolResult.IsError`, not Go `error`.** A tool's
   `Call` returns a `*mcp.ToolResult` with `IsError: true` for expected failures
   (bad args, VLM down, labels not found). Returning a raw `error` from `Call`
   is wrong — it aborts the JSON-RPC request for the client.
3. **Numbers come from Go, not the model.** `measure_image` only lets the VLM
   emit coordinates; `geometry` computes the actual values. Do not move math
   into prompts.
4. **Cache keys are content-addressed (MD5 of the image bytes), never filename
   or URL.** Two images with the same name must not serve each other's answers.
   See `internal/cache/keys.go`.
5. **The VLM is a dependency, injected.** `tools.Deps.Providers` is a `*vlm.Set`.
   Tests swap in `vlm.NewMock`. Never call a backend directly from a tool.

## Layering (dependencies point inward)

```
cmd  ->  tools  ->  {vlm, imgproc, imageio, cache, geometry, mcp}
tools depends on mcp only through the Tool interface (mcp.Tool / mcp.ToolResult)
```

The `mcp` package must never import `tools`, `vlm`, etc. It is the protocol
boundary.

Dev harnesses sit beside this axis and depend inward only:
`cmd/llm-eyes-e2e` -> `{e2e, envfile, mcp}` (real-gateway acceptance),
`cmd/llm-eyes-probe` -> `{probe, envfile, geometry}` (grounding experiments).
No server package may import a harness package.

## Adding a VLM provider

1. Pick a `type` string (e.g. `my_vision`).
2. In a new file under `internal/vlm`, write a `func init() { Register("my_vision", build) }`.
3. Implement `vlm.Provider` (`Name`, `ModelVersion`, `Complete`). `ModelVersion`
   **must** be stable per model build — it is part of the L2 cache key.
4. Unknown types already fall back to the OpenAI-compatible adapter, so many
   backends need only a `config.yml` entry with `type: openai_vision`.

## Adding or changing a tool

- Implement `mcp.Tool` in `internal/tools`.
- Read args defensively: every value arrives as `map[string]any` (numbers are
  `float64`, fields may be missing or wrong-typed). Use the `getString` /
  `getBool` / `getStringSlice` helpers in `common.go`; do not type-assert raw.
- Use `d.preprocess(img, opts)` and `d.resolveImage(src)` — they handle the L1
  and L0 caches for you.
- For measurements, call `d.detectObjects(...)` (handles L2/L3 + convergence),
  then compute with `geometry`.

## Testing conventions

- **No network in tests.** Inject `vlm.NewMock` / `vlm.NewFailingMock`.
- Use `t.TempDir()` for cache roots; the `cache` package is happy with that.
- End-to-end tool tests live in `internal/tools/*_test.go` and assert that a
  second identical call is served from cache (`mock.CallCount() == 1`).
- Pure functions (geometry, parsing, preprocessing) use table-driven subtests.
- Run with `make test`. Do **not** add `-race` to CI: it needs a C compiler and
  this project is intentionally zero-CGO.

## Config & secrets

- `config.yml` references secrets as `${VAR}`; they are expanded from the
  environment at load. Never commit a real key.
- `default_provider: ""` is valid — it falls back to the only enabled provider.
- A loopback `base_url` (localhost / 127.0.0.1 / 0.0.0.0 / 192.168.x) needs no
  `api_key`. Remote URLs require one (validated in `config.Validate`).

## Build & verify

```bash
make build && make test && make check
make all      # cross-compile windows/linux/darwin
make e2e      # matched-pair acceptance against the real gateway (needs UOMA_API_KEY)
```

A healthy `make check` loads config, builds providers, registers the four
tools, and exits 0.

## Dev harness & language purity

Everything executable in this repository is Go. Non-Go harness material
(screenshot capture scripts, a Node E2E runner, a Node grounding probe, a
Python spec converter) was removed in 2026-10-31 after its Go replacements
reproduced the old acceptance results (D-2: 8/8 ok, score=1.00).

**File types enter through an allowlist, not a banlist.** Only the types a Go
project needs may be tracked; anything else is rejected by
`internal/purity/purity_test.go` (runs under `make test`, checks git-tracked
and staged files):

| Extension | Why it is allowed |
|---|---|
| `.go` | the language of this repo |
| `.md` | documentation (README, AGENTS, docs/) |
| `.json` | spec fixtures and data files (examples/, docs/) |
| `.yml` / `.yaml` | CI workflow and config templates |
| `.mod` / `.sum` | go.mod / go.sum |
| `.png` | binary acceptance fixture (testdata/compare/) |
| `.example` | config/env templates (config.yml.example, .env.example) |

Extension-less allowlist: `Makefile`, `LICENSE`, `.gitignore`,
`.gitattributes`. The only path exemption is `docs/archive/` — frozen probe
evidence that must not grow new active material.

To introduce a new file type: either implement it in Go, or amend the
allowlist **in both** `internal/purity/purity_test.go` and the table above,
with a justification for why Go cannot cover it.

**Replacement gate:** before deleting a working harness in favour of a
rewrite, the rewrite must first reproduce the old harness's acceptance
results (for compare_design: the D-2 matched pair, 8/8 ok / score=1.00).

Harness usage:
- `make e2e` — spawns the compiled server, runs `compare_design` on the
  checked-in fixture pair (`testdata/compare/yyhd_reward_section.png` +
  `examples/yyhd_reward_spec.json`) against the real gateway, and enforces the
  score gate. Fresh VLM run by default (`--cached` allows a cached answer).
- `go run ./cmd/llm-eyes-probe` — grounding-accuracy experiment against an
  OpenAI-compatible upstream; synthetic ground truth is drawn with image/draw
  (no browser). Not part of the Makefile.
