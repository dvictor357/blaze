# Blaze

A small Go HTTP toolkit and bounded tool-execution runtime. Define a context-aware Go function once, expose its schema to OpenAI or Anthropic, and execute the model's explicit tool calls in your application.

**Blaze does not call a model, select tools from natural language, or implement a provider's model API.** Your application owns the model connection, conversation, authentication, authorization, and retry policy. Provider helpers convert tool definitions, current calls, and results; they do not scan conversation history.

Requires **Go 1.25.5 or later**. Uses the Go standard library without external module dependencies.

## Run the example

```sh
go run ./examples
```

The example binds to `127.0.0.1:8080`. It prints a random bearer token unless `BLAZE_TOKEN` is set. For repeatable local commands:

```sh
BLAZE_TOKEN=local-development-token go run ./examples
```

In another terminal:

```sh
curl http://127.0.0.1:8080/v1/tools \
  -H 'Authorization: Bearer local-development-token'

curl http://127.0.0.1:8080/v1/invoke \
  -H 'Authorization: Bearer local-development-token' \
  -H 'Content-Type: application/json' \
  -d '{"calls":[{"id":"stats-1","name":"text_stats","input":{"text":"hello world\nBlaze"}}]}'
```

The invocation returns:

```json
{"results":[{"id":"stats-1","name":"text_stats","ok":true,"output":{"characters":17,"lines":2,"words":3}}]}
```

Use TLS and verified credentials before exposing a server beyond loopback. `BLAZE_ADDR` changes the example's listener address; it does not add TLS. Protect discovery as well as execution. The example attaches memory scope from its authenticated identity, never from request JSON.

## Define and execute tools

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"

    "github.com/dvictor357/blaze/adapter"
)

func main() {
    echo := adapter.NewTool("echo", "Return supplied text", map[string]any{
        "type": "object",
        "properties": map[string]any{"text": map[string]any{"type": "string"}},
        "required": []string{"text"},
    }, func(ctx context.Context, input json.RawMessage) (any, error) {
        if err := ctx.Err(); err != nil {
            return nil, err
        }
        var in struct { Text *string `json:"text"` }
        if err := json.Unmarshal(input, &in); err != nil {
            return nil, err
        }
        if in.Text == nil {
            return nil, fmt.Errorf("text is required")
        }
        return map[string]string{"text": *in.Text}, nil
    })
    ex, err := adapter.NewExecutor(adapter.Config{}, echo)
    if err != nil {
        log.Fatal(err)
    }
    results, err := ex.Execute(context.Background(), []adapter.Call{
        {ID: "echo-1", Name: "echo", Input: json.RawMessage(`{"text":"hello"}`)},
    })
    if err != nil {
        log.Fatal(err)
    }
    encoded, err := json.Marshal(results)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(string(encoded))
}
```

`NewExecutor` rejects invalid/duplicate names, nil handlers, and malformed schema metadata. Execution validates the entire batch before running any member: unique nonempty IDs, known names, object-shaped JSON, and input/count limits. A later invalid call cannot leave an earlier call partially executed.

**Schemas are descriptive metadata, not runtime JSON Schema validation.** Each handler must validate its own input and authorization rules. Built-in tools reject unknown input fields. Handler error text is returned to callers and models; do not put secrets in errors. Panics and result-encoding failures become structured failures.

For HTTP hosting, register `adapter.InvokeHandler(ex)` and `adapter.ListToolsHandler(ex)` on your authenticated routes. See [examples/main.go](examples/main.go) for a complete server and [adapter/README.md](adapter/README.md) for the wire contract.

## Streaming and execution limits

Set `"stream":true` in the same invocation request. Use `curl -N` to see flushed events:

```sh
curl -N http://127.0.0.1:8080/v1/invoke \
  -H 'Authorization: Bearer local-development-token' \
  -H 'Content-Type: application/json' \
  -d '{"stream":true,"calls":[{"id":"stats-1","name":"text_stats","input":{"text":"stream"}}]}'
```

The response uses `text/event-stream`: a `result` event per completed call and a final `done` event. **These are Blaze execution events, not OpenAI or Anthropic model-stream events.** Results flush before the next call runs; no adapter producer goroutine or channel is created.

| Executor setting | Default |
|---|---:|
| Calls per batch | 16 |
| Input bytes per call | 32 KiB |
| Encoded output bytes per call | 256 KiB |
| Aggregate successful output bytes | 1 MiB |
| HTTP request body | 1 MiB |
| Per-call context deadline | 30 seconds |
| Batch context deadline | 60 seconds |

Configure these through `adapter.Config`. Handler execution is synchronous and cancellation is **cooperative**: pass the supplied context to I/O and check it in long loops. Go cannot forcibly terminate arbitrary callbacks. Output limits are checked after encoding; they are not a sandbox or a hard limit on allocations made inside handlers. Applications still need admission/concurrency controls appropriate to their workload.

There is no exactly-once guarantee. Retrying an accepted invocation repeats its side effects; use application-owned idempotency keys for sensitive operations.

## HTTP routing and middleware

Routing uses `net/http.ServeMux`, including its method handling, specificity, escaping, conflict detection, and trailing-slash redirects:

```go
e := blaze.New()
e.Wrap(blaze.CORS(blaze.CORSConfig{
    AllowOrigins: []string{"https://app.example.com"},
    AllowMethods: []string{"GET", "POST", "OPTIONS"},
    AllowHeaders: []string{"Authorization", "Content-Type"},
}))
e.Use(blaze.Recovery(), blaze.Logger())
e.GET("/users/{id}", func(c *blaze.Context) error {
    return c.JSON(200, map[string]string{"id": c.Param("id")})
})
api := e.Group("/api")
api.GET("/status", func(c *blaze.Context) error {
    return c.String(200, "ok")
})
```

Use `/{$}` for the exact root, `{id}` for one segment, and `{path...}` for a remaining-path wildcard. Duplicate or ambiguous patterns panic at registration. GET also serves HEAD. CORS belongs in `Wrap`, outside method dispatch, so preflights do not need explicit OPTIONS routes. CORS is not authentication.

`Use` and group middleware apply to subsequently registered routes. Nested groups copy parent middleware at creation. `Wrap` must be configured before serving. Configure routes and middleware before accepting traffic.

`BindJSON` limits bodies to 1 MiB and accepts exactly one JSON value. `BindJSONLimit` changes the limit; `BindJSONStrict` also rejects unknown fields. `StreamJSON` is cancellation-aware NDJSON (`application/x-ndjson`), not SSE; `Context.SSE` writes SSE synchronously. External channel producers must observe the same request context.

`Engine.Server(addr)` returns a configurable `http.Server`; `Listen` uses its defaults: 5s read-header, 30s read, 60s write, 120s idle, and 64 KiB request-header limits. Streams use a 10s per-write deadline. Hosting applications own shutdown and TLS.

## Built-in tools

| Tools | Contract |
|---|---|
| [Web fetch, read, search](docs/tools/web.md) | Shared client, bounded responses, validated DNS and redirects, private destinations blocked by default |
| [Memory](docs/tools/memory.md) | Explicit store ownership, trusted context scopes, TTL, counters/lists, bounded process-local state |
| [Datetime](docs/tools/datetime.md) | Date parsing, formatting, differences, and duration addition |
| [JSON query](docs/tools/json-query.md) | Small path/filter query language; not a complete jq implementation |

Use `tool.NewWebClient(tool.WebClientConfig{})` and pass it to all three web tools to share its transport. Memory needs `tool.NewMemoryStore`, `tool.NewMemoryTool(store)`, and a scope set by trusted server code with `tool.WithMemoryScope`.

## Provider integration

1. Send `Tool.ToOpenAI()` or `Tool.ToAnthropic()` definitions to your chosen model using your own client.
2. Convert only the model's **current** calls with `adapter.OpenAICalls` or `adapter.AnthropicCalls`.
3. Execute the explicit calls.
4. Return `adapter.OpenAIToolMessages` or `adapter.AnthropicToolResultMessage` to the model, alongside its preceding assistant message.

See the [OpenAI](docs/adapters/openai.md) and [Anthropic](docs/adapters/anthropic.md) guides. These helpers do not establish provider connections, perform inference, or automatically replay old calls.

## Breaking changes

- Removed the fake chat adapters, chat-completion response types, canned text, token estimates, and legacy aliases. Use an executor plus explicit invocation; model communication stays in your application.
- Tool handlers now receive `context.Context`. `ListToolsHandler` takes an executor, not a list of tools.
- Web-tool constructors take a `*tool.WebClient`; nil means a new secure-default client. URLs must be absolute HTTP(S). Local destinations require an explicit network-policy exception.
- Memory has no package-global store. Supply a store and trusted context scope; enumeration/clear are scoped. Unsupported list-pop aliases are not provided.
- Routes use ServeMux brace patterns instead of colon/star segments. Root-only routes use `/{$}`. Register CORS through `Wrap`, not `Use`.
- Removed the unused chainable `Context.Status`; response methods take explicit status codes. `StreamJSON` is correctly labeled NDJSON.

## Verification and performance

```sh
go build ./...
go vet ./...
go test -race ./...
go test -run '^$' -bench . -benchmem ./...
```

Benchmarks cover engine dispatch, not network throughput or model/tool latency. The mixed benchmark performs three dispatches per iteration; compare like workloads. No cross-framework performance ranking is claimed.

## License

[MIT](LICENSE).
