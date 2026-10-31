# Blaze documentation

Blaze is a Go HTTP toolkit and tool-execution runtime. Your application owns model requests, conversation state, credentials, authorization, and retries. Blaze runs explicitly selected calls; it is not an OpenAI/Anthropic model server.

## Runtime and HTTP

- [Project README](../README.md): setup, routing, limits, breaking changes, verification.
- [Execution API](../adapter/README.md): tool registration, full-batch validation, invocation/discovery, JSON/SSE results, cancellation.
- [Runnable server](../examples/main.go): localhost listener, bearer authentication, trusted memory scope, custom text-statistics tool, and built-ins.

## Provider conversions

- [OpenAI](adapters/openai.md): current function calls to explicit invocations; correlated tool-role results.
- [Anthropic](adapters/anthropic.md): assistant tool-use blocks to explicit invocations; user tool-result reply.

Both guides describe pure conversions, not HTTP model endpoints or model-stream protocols. Provider clients and external API calls remain application-owned.

## Built-in tools

| Guide | Scope |
|---|---|
| [Web](tools/web.md) | Secure HTTP egress, raw fetch, best-effort HTML reading, DuckDuckGo search |
| [Memory](tools/memory.md) | Owned stores, trusted request scopes, quotas, TTL, counters, lists |
| [Datetime](tools/datetime.md) | Date/time actions and supported formats |
| [JSON query](tools/json-query.md) | Path/filter syntax and transformation actions |

Handlers receive a cancellation context and JSON object. Schema metadata informs the model; it is not generic runtime schema validation. Authenticate and authorize callers before dispatch and never derive memory scope from untrusted request JSON.

## License

[MIT](../LICENSE).
