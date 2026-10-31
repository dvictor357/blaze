# Tool execution and provider conversion

`adapter` owns tool definitions, a validated executor, HTTP invocation/discovery handlers, and pure OpenAI/Anthropic conversions. It does not implement a model endpoint or connect to a provider.

## Public entry points

- `NewTool(name, description, schema, Handler) Tool`, where `Handler` is `func(context.Context, json.RawMessage) (any, error)`.
- `NewExecutor(Config, ...Tool) (*Executor, error)` validates registration and snapshots schema metadata.
- `Executor.Validate([]Call) error` rejects an invalid batch before execution.
- `Executor.Execute(context.Context, []Call) ([]Result, error)` executes in order.
- `Executor.Run(context.Context, []Call, func(Result) error) error` emits each result synchronously, before the next call starts. An emit error stops execution.
- `InvokeHandler(*Executor) blaze.HandlerFunc` and `ListToolsHandler(*Executor) blaze.HandlerFunc` can be mounted on authenticated engine/group routes.

A `Call` contains `ID`, `Name`, and `Input` (`json.RawMessage`). Empty input means `{}`. IDs must be unique within a batch, valid UTF-8, nonempty, and at most 128 bytes. Names must identify registered tools. Inputs must be valid JSON objects within the configured byte limit. Schema metadata is not runtime validation: handlers validate domain inputs.

## HTTP API

The route paths are chosen by your application. The shipped example mounts `GET /v1/tools` and `POST /v1/invoke` behind bearer authentication.

Discovery returns `{ "openai": [...], "anthropic": [...], "count": N }` containing tool definitions for the application's model request.

Invoke requires `Content-Type: application/json` and accepts exactly this shape:

```json
{"calls":[{"id":"a","name":"text_stats","input":{"text":"hello"}}],"stream":false}
```

Unknown fields, including `messages` or caller-supplied scope fields, are rejected. The full batch is validated before any handler runs.

Non-streaming success is HTTP 200 with `{ "results": [...] }`. Each result contains its `id`, `name`, `ok`, and either `output` or an `error` object with `code` and `message`. Per-call handler, timeout, panic, encoding, and output-budget failures are correlated results, not whole-request validation failures.

| Request failure | HTTP status |
|---|---:|
| Invalid JSON, unknown fields, or trailing JSON | 400 |
| Body exceeds configured limit | 413 |
| Missing/wrong content type | 415 |
| Invalid batch, duplicate ID, unknown tool, or oversized input | 422 |

Applications own authentication responses. Handler errors are exposed in result messages, capped to a bounded length; do not include credentials or internal secrets. Panics and result-encoding failures use generic messages. Already committed HTTP responses are not overwritten with error text.

## Streaming

Use `"stream":true` in the same request. Validation still occurs before response commitment. A completed stream is `Content-Type: text/event-stream` with frames such as:

```text
event: result
data: {"id":"a","name":"text_stats","ok":true,"output":{"characters":5,"lines":1,"words":1}}

event: done
data: {"count":1}

```

These are **Blaze execution events**, not provider model-stream events. Each result flushes before the next call starts. A disconnected client or failed write stops later calls. Incomplete/disconnected streams do not promise a final `done` event. No producer goroutine is created.

Execution is sequential and synchronous. Context deadlines are cooperative: a callback that ignores cancellation can delay its caller. Do not add a background goroutine merely to hide an uncooperative callback.

## Default budgets

`Config` zero values select: 16 calls, 32 KiB input/call, 256 KiB encoded output/call, 1 MiB aggregate successful output, 1 MiB HTTP body, 30s call context, and 60s batch context. Negative configuration values are rejected. Once the aggregate output budget is exceeded, later calls are skipped; once the batch deadline expires, remaining calls are not executed.

Output bounds apply after JSON encoding, not to arbitrary allocations inside a handler. Supply application concurrency/admission limits and idempotency where needed. Duplicate IDs are rejected within a batch; retries across requests are **not** deduplicated.

## Providers

- [OpenAI function calls and tool-role results](../docs/adapters/openai.md)
- [Anthropic assistant tool-use and user tool-result blocks](../docs/adapters/anthropic.md)

Conversion takes only explicitly supplied current calls and preserves call IDs. There is no conversation-history scanner, fabricated assistant response, or token accounting.
