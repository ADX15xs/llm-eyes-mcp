# llm-eyes-mcp

A zero-dependency, zero-CGO **MCP stdio server** that gives a text-only LLM a
pair of eyes. It speaks the Model Context Protocol over NDJSON on stdin/stdout
and exposes three focused tools:

| Tool | Purpose | Pipeline |
|------|---------|----------|
| `measure_image` | Exact pixel geometry: distance, alignment, area. The VLM supplies **only coordinates**; all arithmetic is done deterministically in Go. | **hard** (lossless, CLAHE + unsharp) |
| `describe_image` | Semantic understanding: what is in the picture, layout, style. | **soft** (aggressive JPEG downscale) |
| `extract_text` | OCR: pull the literal text out of an image. | **soft** |

Because coordinates come from the model but numbers come from code, measurement
results are reproducible rather than estimated.

## Why

A vision-capable LLM is expensive and slow. Most of the time the same image is
asked about repeatedly. `llm-eyes-mcp` sits between the agent and the vision
backend and **caches aggressively** — original bytes (L0), preprocessed
renditions (L1), VLM responses (L2), and computed geometry (L3) — so the second
question about a picture is nearly free and costs zero API calls.

## Build

Requires Go 1.22+. The binary is `CGO_ENABLED=0`, so it is a single static file
and builds cleanly on any platform.

```bash
make build          # -> bin/llm-eyes-mcp  (or .exe on Windows)
make test           # run the full test suite
make check          # build + validate config & providers, then exit
make all            # cross-compile windows / linux / darwin
```

The resulting binary is well under **20 MB** (pure stdlib + `golang.org/x/image`
+ `gopkg.in/yaml.v3` + `modernc.org/sqlite`).

## Configure

Copy `config.yml.example` to `config.yml` and edit. Secrets are referenced as
`${VAR}` and expanded from the environment (see `.env.example`). Config
resolution order:

1. `--config <path>`
2. `$LLM_EYES_CONFIG`
3. `config.yml` next to the executable
4. `./config.yml` in the working directory

```bash
./bin/llm-eyes-mcp --check --config config.yml   # validate before deploying
```

## Run

The server is a long-running stdio process. Your MCP client launches it:

```bash
./bin/llm-eyes-mcp --config config.yml
```

> **stdout is reserved** for the JSON-RPC NDJSON stream. Every diagnostic goes to
> stderr. Never `fmt.Println` to stdout from server code.

## Connect an MCP client

MCP clients launch the server as a subprocess. **Use absolute paths** for both
the `command` (the binary) and the `--config` argument — relative paths resolve
against the client's own working directory, which is usually not yours.

### Claude Desktop (`claude_desktop_config.json`)

```json
{
  "mcpServers": {
    "llm-eyes-mcp": {
      "command": "/absolute/path/to/llm-eyes-mcp",
      "args": ["--config", "/absolute/path/to/config.yml"]
    }
  }
}
```

### VS Code (`settings.json` → `mcp.servers`)

```json
{
  "mcp": {
    "servers": {
      "llm-eyes-mcp": {
        "command": "C:\\absolute\\path\\to\\llm-eyes-mcp.exe",
        "args": ["--config", "C:\\absolute\\path\\to\\config.yml"]
      }
    }
  }
}
```

### Generic / other clients

Any client that supports `"transport": "stdio"` works with the same shape: a
`command` pointing at the binary and `args: ["--config", "<absolute path>"]`.

### Image inputs the tools accept

Every tool takes `image_source`, which may be:

- an `http(s)://` URL
- an absolute file path or `file://` URI
- a `data:` URI or raw base64
- a **32-character `image_id`** returned by a previous call (cheapest — skips
  re-upload and hits the cache, because the original bytes are archived in L0)

## How caching keeps it cheap

| Tier | Stores | Backed by | Invalidation |
|------|--------|-----------|--------------|
| L0 | original bytes | disk | never (content-addressed) |
| L1 | preprocessed rendition per intent | disk (24 h, 1 GiB LRU) | TTL / idle / byte budget |
| L2 | VLM response per tool+model+params | SQLite (7 d, 100 MiB) | TTL / byte budget / **credential change** |
| L3 | computed geometry | in-memory LRU | end of session |

If the API key or endpoint changes, L2 is purged automatically — answers from a
different backend must never be reused.

## Project layout

```
cmd/llm-eyes-mcp   entrypoint: flags, config, providers, cache, server wiring
internal/
  config   config load / env-expand / validate / credential fingerprint
  mcp      protocol layer: NDJSON framing, concurrent dispatch, tool registry
  imageio  universal image loader (URL / file / data-uri / base64 / archive id)
  imgproc  pure-Go preprocessing (CLAHE, unsharp, scaling)
  vlm      vision backend adapter (OpenAI-compatible + mock)
  tools    the three tools + parse/geometry/measure logic
  cache    the four-tier cache
  geometry deterministic geometry math
```

## Testing

Tests are table-driven and **never touch the network** — the VLM is injected via
`vlm.NewMock`, and the cache uses `t.TempDir()`. Run everything with:

```bash
make test
```

Coverage includes the protocol framing (§7 of `docs/MCP_DEV_GUIDE.md`),
coordinate parsing, the four cache tiers, and end-to-end tool runs that assert a
repeat call is served from cache (the mock's `CallCount` stays at 1).

## License

MIT.
