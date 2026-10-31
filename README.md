# Blaze

[![CI](https://github.com/dvictor357/blaze/actions/workflows/ci.yml/badge.svg)](https://github.com/dvictor357/blaze/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/dvictor357/blaze)](https://goreportcard.com/report/github.com/dvictor357/blaze)
[![Go Reference](https://pkg.go.dev/badge/github.com/dvictor357/blaze.svg)](https://pkg.go.dev/github.com/dvictor357/blaze)

**Write a Go function once, expose it safely to OpenAI, Anthropic and MCP.**

## Why

Every LLM app with tools ends up writing the same glue: a JSON Schema per provider, a loop that turns the model's tool calls into function calls, and the defensive code around them. That defensive part is the one that tends to get skipped. Model output is untrusted input: arguments can be malformed, a call can hang, a handler can panic, a `fetch` tool can be pointed at `169.254.169.254`.

Blaze is a small, zero-dependency Go toolkit for that layer. You register plain Go functions once. Blaze gives you the OpenAI and Anthropic tool definitions, converts the model's calls, validates arguments against the schema, and runs each call with timeouts, size limits and panic recovery. The same tools can be served to Claude Desktop, Claude Code or Cursor as an MCP server. Blaze never talks to the model itself: your app keeps its client, conversation and auth.

## Quickstart

```sh
go get github.com/dvictor357/blaze
```

```go
weather := adapter.NewTool("get_weather", "Current weather for a city", map[string]any{
    "type":       "object",
    "properties": map[string]any{"city": map[string]any{"type": "string"}},
    "required":   []string{"city"},
}, func(ctx context.Context, in json.RawMessage) (any, error) {
    var args struct{ City string `json:"city"` }
    if err := json.Unmarshal(in, &args); err != nil {
        return nil, err
    }
    return map[string]any{"city": args.City, "temp_c": 21}, nil
})
ex, err := adapter.NewExecutor(adapter.Config{}, weather)

def := ex.Tools()[0].ToAnthropic()             // or .ToOpenAI(): send it to your model
calls, err := adapter.AnthropicCalls(content)  // the model's tool_use blocks
results, err := ex.Execute(ctx, calls)         // validated, bounded, panic-safe
reply, err := adapter.AnthropicToolResultMessage(results)
```

The same `ex` serves MCP: `blazemcp.ServeStdio(ctx, ex, blazemcp.Options{})`. Full round trips are in [examples/agent-loop](examples/agent-loop/main.go).

## Features

- **One definition, three targets.** `ToOpenAI()`, `ToAnthropic()`, and an [MCP server](docs/mcp.md) over stdio or Streamable HTTP, from the same `adapter.Tool`.
- **Runtime argument validation.** Arguments are checked against the tool's JSON Schema before the handler runs. Violations come back as a tool error the model can read and fix: `$.units: must be one of ["metric","imperial"], got "kelvin"`. [Details](adapter/README.md#schema-validation).
- **Bounded execution.** Per-call and per-batch timeouts, input/output size limits, whole-batch validation before anything runs, and panics turned into structured errors.
- **Safe built-in tools.** `web_fetch`, `web_read` and `web_search` refuse private, loopback and metadata addresses (with DNS-rebinding protection); `memory` is scoped per user by your server, never by the model; plus `datetime` and `json_query`.
- **Provider conversions, not a provider client.** Pure functions between Blaze and the OpenAI/Anthropic wire shapes. Use the official SDKs or raw HTTP; Blaze stays out of the conversation.
- **HTTP endpoint included.** `adapter.InvokeHandler` and `adapter.ListToolsHandler` expose the executor as JSON or SSE, on a tiny `net/http`-based router.
- **Zero dependencies.** The core module uses only the standard library. MCP lives in its own module ([`github.com/dvictor357/blaze/mcp`](mcp)) on top of the official MCP Go SDK.

Requires Go 1.25.5 or later.

## Use it with MCP clients

```sh
cd examples/mcp-server && go build -o blaze-mcp .
claude mcp add blaze -- "$PWD/blaze-mcp"        # Claude Code
```

Claude Desktop and Cursor take the same binary:

```json
{ "mcpServers": { "blaze": { "command": "/path/to/blaze-mcp" } } }
```

Streamable HTTP with bearer auth, per-user memory scopes and the full set of client configs: [examples/mcp-server](examples/mcp-server).

## Examples

| Example | What it shows |
|---|---|
| [examples/agent-loop](examples/agent-loop/main.go) | Full model round trip with OpenAI and Anthropic using the built-in tools. Reads `OPENAI_API_KEY` / `ANTHROPIC_API_KEY`, skips a provider without one. |
| [examples/mcp-server](examples/mcp-server) | The tools as an MCP server for Claude Desktop, Claude Code and Cursor (stdio or HTTP). |
| [examples/main.go](examples/main.go) | An HTTP tool server: `GET /v1/tools`, `POST /v1/invoke`, bearer auth, SSE streaming. |

```sh
OPENAI_API_KEY=... ANTHROPIC_API_KEY=... go run ./examples/agent-loop
BLAZE_TOKEN=dev go run ./examples     # then: curl -H 'Authorization: Bearer dev' 127.0.0.1:8080/v1/tools
```

## Built-in tools

| Tool | Notes |
|---|---|
| [`web_fetch`, `web_read`, `web_search`](docs/tools/web.md) | Shared `tool.WebClient`; validated DNS and redirects; private destinations blocked by default; bounded responses |
| [`memory`](docs/tools/memory.md) | Key-value, counters, lists, TTL; explicit store ownership; scope from `tool.WithMemoryScope`, never from tool input |
| [`datetime`](docs/tools/datetime.md) | Current time, parsing, formatting, differences, durations |
| [`json_query`](docs/tools/json-query.md) | Small path/filter language for extracting data from JSON |

## Documentation

- [Security model](docs/security.md): what Blaze guarantees, the defaults, and what remains your job.
- [Execution API](adapter/README.md): executor, schema validation, HTTP/SSE invocation contract.
- [OpenAI](docs/adapters/openai.md) and [Anthropic](docs/adapters/anthropic.md) conversions.
- [MCP server](docs/mcp.md).
- [Testing, CI and live provider proof](docs/testing.md).
- [Changelog](CHANGELOG.md).

The root package `blaze` is a small HTTP toolkit on `net/http.ServeMux` (groups, middleware, CORS, JSON binding with limits, NDJSON and SSE). See the [package docs](https://pkg.go.dev/github.com/dvictor357/blaze).

## Status

v0.x: the API may still change between minor versions; changes are listed in the [changelog](CHANGELOG.md). Issues and pull requests are welcome.

## License

[MIT](LICENSE).

---

Need help wiring this into your backend? Reach out: https://x.com/dvictor3579
