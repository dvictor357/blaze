# Changelog

All notable changes to this project are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [semantic versioning](https://semver.org/). Before 1.0, minor versions may contain breaking changes.

## [Unreleased]

### Added

- Local inventory demo: a transactional SQLite `reserve_inventory` tool shared by HTTP and MCP, a browser dashboard, and a real Claude Code CLI integration. Includes schema/business-rule rejection examples, application-level order idempotency, and race-tested stock invariants. The separate example module requires CGO; the core remains dependency-free.

## [0.1.0] - 2026-11-01

First tagged release. Two modules are tagged together: `github.com/dvictor357/blaze` (`v0.1.0`) and `github.com/dvictor357/blaze/mcp` (`mcp/v0.1.0`).

### Added

- `adapter.Executor`: registers tools once and runs batches of calls with whole-batch validation, per-call and per-batch timeouts, input/output size limits, an aggregate output budget, and panic recovery.
- Runtime validation of call arguments against each tool's JSON Schema. Violations are per-call `invalid_arguments` results that list problems with JSON paths; the handler is not called. Unsupported keywords such as `$ref` are rejected at registration. `Config.DisableSchemaValidation` opts out.
- Provider conversions: `Tool.ToOpenAI`, `Tool.ToAnthropic`, `OpenAICalls`, `OpenAIToolMessages`, `AnthropicCalls`, `AnthropicToolResults`, `AnthropicToolResultMessage`.
- HTTP endpoints: `adapter.InvokeHandler` (JSON or SSE results) and `adapter.ListToolsHandler`.
- MCP module `github.com/dvictor357/blaze/mcp`: `NewServer`, `ServeStdio` and `NewHTTPHandler` (stateless Streamable HTTP with body limits, DNS-rebinding and cross-origin protection), built on the official MCP Go SDK. `Options.Context` attaches trusted per-caller values such as the memory scope.
- Built-in tools: `web_fetch`, `web_read`, `web_search` (shared `WebClient` with SSRF protection), `memory` (owned stores, trusted scopes, quotas, TTL), `datetime`, `json_query`. Their schemas set `"additionalProperties": false`.
- `blaze` HTTP toolkit on `net/http.ServeMux`: groups, per-route and engine-wide middleware, CORS, recovery, logging, bounded JSON binding, NDJSON and SSE, safe server timeouts.
- Examples: `examples/agent-loop` (OpenAI and Anthropic round trips), `examples/mcp-server` (Claude Desktop, Claude Code, Cursor), and `examples` (HTTP tool server).
- Docs: security model, MCP, provider guides, built-in tools, testing and live provider proof.
- CI on Go 1.25.5 and stable for every module, a zero-dependency check for the core module, and a manual live-provider workflow.

### Changed (breaking, relative to the untagged main branch)

- Removed the fake chat adapters, chat-completion response types, canned text, token estimates and legacy aliases. Use an executor plus explicit invocation; model communication stays in your application.
- Tool handlers receive `context.Context`. `ListToolsHandler` takes an executor, not a list of tools.
- Tool arguments are now validated against the input schema before the handler runs (see above).
- Web-tool constructors take a `*tool.WebClient`; nil means a new secure-default client. URLs must be absolute HTTP(S). Local destinations require an explicit network-policy exception.
- Memory has no package-global store. Supply a store and a trusted context scope; enumeration and clear are scoped. Unsupported list-pop aliases are not provided.
- Routes use `ServeMux` brace patterns instead of colon/star segments; root-only routes use `/{$}`. Register CORS with `Wrap`, not `Use`.
- Removed the unused chainable `Context.Status`; response methods take explicit status codes. `StreamJSON` is labeled NDJSON.

[0.1.0]: https://github.com/dvictor357/blaze/releases/tag/v0.1.0
