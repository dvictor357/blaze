# MCP server

The `github.com/dvictor357/blaze/mcp` module serves the tools of an `adapter.Executor` as a [Model Context Protocol](https://modelcontextprotocol.io) server. The same registered Go tools you send to OpenAI or Anthropic become MCP tools, with no second definition.

It is a separate Go module built on the official [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk), so the core `blaze` module keeps zero external dependencies. Install it only if you need MCP:

```sh
go get github.com/dvictor357/blaze/mcp
```

## API

```go
srv := blazemcp.NewServer(ex, opts)          // *mcp.Server from the SDK, any transport
err := blazemcp.ServeStdio(ctx, ex, opts)    // stdio, for desktop clients
h   := blazemcp.NewHTTPHandler(ex, opts)     // Streamable HTTP http.Handler
```

`Options`:

| Field | Purpose |
|---|---|
| `Name`, `Version` | Server identity sent to clients (defaults `blaze`, `dev`). |
| `Instructions` | Optional guidance sent at initialization. |
| `Context` | Runs before each `tools/call`; returns the context the tool runs in. Attach trusted per-caller values here, such as `tool.WithMemoryScope`. An error rejects the call. |
| `Logger` | SDK diagnostics (`*slog.Logger`); never write to stdout in stdio mode. |

A runnable server with config snippets for Claude Desktop, Claude Code and Cursor is in [examples/mcp-server](../examples/mcp-server).

## Behavior

- `tools/list` returns every executor tool with its name, description and normalized input schema.
- `tools/call` runs through `Executor.Execute` as a batch of one. Input limits, [schema validation](../adapter/README.md#schema-validation), per-call timeout, output limits and panic recovery all apply.
- Success returns the tool's JSON output as text content, and also as `structuredContent` when it is a JSON object.
- Failures (invalid arguments, handler error, timeout, panic, oversized input or output) return `isError: true` with `{"error":{"code":...,"message":...}}`, so the model sees what went wrong and can retry. Panic messages stay generic.
- An unknown tool name, a canceled request, or an error from `Options.Context` is a JSON-RPC error.

## Security

The MCP layer adds no new trust: it calls the same executor, so the guarantees in [security.md](security.md) hold unchanged. MCP-specific points:

- **Memory scope.** Without `Options.Context`, the memory tool fails with `ErrMemoryScopeRequired`. Derive the scope from a verified identity: over stdio the launching user is the only caller, so a fixed scope is fine; over HTTP, use `req.Extra.TokenInfo` set by authentication middleware (for example `auth.RequireBearerToken` from the SDK). Never take a scope from tool arguments.
- **HTTP transport.** `NewHTTPHandler` is stateless and answers each POST with a single JSON response, so it works behind normal server write timeouts; GET and DELETE return 405. Request bodies are capped at `Config.MaxRequestBytes` (413 above it). Requests arriving on a loopback address with a non-loopback `Host` header are rejected (DNS rebinding), and cross-origin browser requests are rejected by `http.CrossOriginProtection`.
- **Authentication is yours.** The handler authenticates nobody. Wrap it before exposing it beyond loopback, and serve it over TLS. Give your `http.Server` read/write timeouts; the example sets the write timeout to the executor's `MaxDuration` plus slack.
- **stdio.** Standard output carries the protocol. Log to stderr. The executor logs recovered panics with the standard `log` package, which writes to stderr by default.

For transport settings the defaults do not cover (sessions, SSE responses), build your own handler from `NewServer` with `mcp.NewStreamableHTTPHandler`, and keep the body limit and the protections above.

## Inventory business-tool demo

[`examples/inventory-demo`](../examples/inventory-demo) exposes a transactional `reserve_inventory` Go tool through HTTP and MCP using one executor, with a browser dashboard showing real SQLite state.

Requires Go 1.25.5+, CGO, and a C compiler (Xcode Command Line Tools on macOS). From the repository root:

```sh
cd examples/inventory-demo
CGO_ENABLED=1 go run . -addr 127.0.0.1:18084 -db inventory.db
```

Open <http://127.0.0.1:18084>. The server generates and prints a bearer token, or uses `BLAZE_DEMO_TOKEN`. The dashboard receives that token in its HTML and authenticates `/api/*` requests; MCP clients use the same bearer header at `/mcp`. This is a **loopback-only, single-user demo**, not production authentication. Do not expose it through a public reverse proxy.

A new database starts with eight keyboards (`KB-001`). Reserve three through HTTP, then use **Ask Claude Code** to reserve two through MCP: three remain available and five are reserved. The Claude action runs the installed, authenticated `claude` CLI with only the inventory MCP tool enabled, no built-in tools, no hooks, and no session persistence. It uses the `sonnet` alias and may consume model usage. The displayed tool calls and reply are actual CLI output; failures never fall back to simulated responses. HTTP inventory features need no model access.

The safeguard buttons send real requests: quantity −2 fails Blaze's schema validation before the handler runs; quantity ten fails the handler's stock rule. Both leave stock unchanged. A business rejection uses Blaze's `handler_error` code; its message and activity entry identify `insufficient_stock`.

SQL transactions commit the stock decrement, reservation, and success event together. **Order idempotency is business-handler logic, not a Blaze executor guarantee:** identical order/SKU/quantity replays return the existing reservation without decrementing stock; conflicting order reuse fails. Existing databases retain their state on restart. Use a new database path for a clean recording; there is no reset endpoint.

`GET /api/state` returns product, reservations, and newest-first activity; `POST /api/invoke` uses the standard Blaze invocation contract. HTTP request/schema rejections and business outcomes are audited. MCP protocol/schema failures that never reach the handler are not included in this demo's activity feed. Claude requests are serialized and time-bounded; if a request fails after execution, check inventory before retrying.

Run `CGO_ENABLED=1 go test -race ./...` inside this module for concurrent no-oversell, duplicate-order, rejection, and persistence checks. These tests never invoke Claude or make model calls.
