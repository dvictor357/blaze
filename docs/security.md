# Security model

Blaze runs tool calls that a language model asked for. Model output is untrusted input, so every entry point (direct `Executor` calls, the HTTP invocation endpoint, and the MCP server) goes through the same checks. This page lists what Blaze guarantees and what stays your job.

## What Blaze does not do

Blaze does not call a model, choose tools from natural language, or implement a provider's model API. Your application owns the model connection, the conversation, authentication, authorization, and retry policy. The provider helpers convert tool definitions, the model's **current** calls, and results; they never scan conversation history or replay old calls.

## Registration

`NewExecutor` rejects, before anything is served:

- invalid tool names (1-64 of letters, digits, `_`, `-`) and duplicates;
- nil handlers;
- input schemas that are not JSON objects with `"type": "object"`;
- schemas with invalid keywords (bad regex, negative lengths, unknown types) or with keywords the validator cannot enforce, such as `$ref`, unless `Config.DisableSchemaValidation` is set. Unsupported keywords are refused rather than silently skipped.

Tools and schemas are copied at registration; later changes to your values do not affect the executor.

## Batch validation

A batch is validated as a whole before any call runs: non-empty, at most `MaxCalls`, unique non-empty IDs (valid UTF-8, at most 128 bytes), registered tool names, and inputs that are JSON objects within `MaxInputBytes`. A later invalid call cannot leave an earlier one half-executed: the batch is rejected with nothing run.

## Argument validation

Each call's arguments are then validated against the tool's input schema. A violation becomes a per-call `invalid_arguments` result listing up to 8 problems with JSON paths, and the handler is not called. See [schema validation](../adapter/README.md#schema-validation) for the supported keywords. Schemas cannot express authorization or most domain rules, so handlers should still check those. Built-in tools also decode strictly and reject unknown fields.

## Execution limits

| Setting (`adapter.Config`) | Default |
|---|---:|
| Calls per batch (`MaxCalls`) | 16 |
| Input bytes per call (`MaxInputBytes`) | 32 KiB |
| Encoded output bytes per call (`MaxResultBytes`) | 256 KiB |
| Aggregate successful output per batch (`MaxTotalResultBytes`) | 1 MiB |
| HTTP request body (`MaxRequestBytes`, also the MCP HTTP body limit) | 1 MiB |
| Per-call context deadline (`CallTimeout`) | 30 s |
| Batch context deadline (`MaxDuration`) | 60 s |

Negative values are rejected. Once the batch deadline passes or the output budget is exhausted, remaining calls are reported as failed without running.

Handler execution is synchronous and cancellation is **cooperative**: pass the supplied context to I/O and check it in long loops. Go cannot forcibly stop a function, so a handler that ignores its context delays its caller. Output limits apply after JSON encoding; they are not a sandbox and do not bound allocations inside a handler. Add admission and concurrency limits that fit your workload.

## Failures

Handler errors, timeouts, cancellation, panics, oversized results, and encoding failures are correlated per-call results, never a crash. Panics are recovered and logged; the caller only sees a generic "tool panicked". Handler error text **is** returned to callers and models (capped at 1 KiB): do not put secrets in errors.

## No exactly-once

Every accepted call executes every time. Retrying a batch repeats its side effects. Make sensitive tools idempotent, for example with your own idempotency keys.

## Web tools (SSRF)

`web_fetch`, `web_read` and `web_search` share a `tool.WebClient` whose default policy:

- blocks private, loopback, link-local, unspecified, multicast and special-purpose addresses, including IPv4-mapped IPv6 and translation ranges;
- resolves DNS itself and dials the checked IP, so a second resolution cannot rebind to an internal address; redirects are re-checked;
- ignores ambient `HTTP_PROXY`/`HTTPS_PROXY`;
- rejects non-HTTP(S) URLs and URL credentials, caps redirects, headers and bodies, and drops model-supplied sensitive headers across origins.

This is defense in depth, not a replacement for an egress firewall. Exceptions (`WebClientConfig.AllowedNetworks`) are per client, never process-global. Details: [web tools](tools/web.md).

## Memory scope

The memory tool has no namespace argument. Its scope comes only from the context, set by trusted server code with `tool.WithMemoryScope` after authenticating the caller, never from request JSON or tool arguments. Without a scope the tool fails with `ErrMemoryScopeRequired`. A caller cannot read, list or clear another scope's keys. Stores are explicitly owned (no package-global store), bounded by quotas, process-local and lost on restart. Details: [memory](tools/memory.md).

## HTTP hosting

- `adapter.InvokeHandler` accepts only `application/json` with exactly `{"calls": [...], "stream": bool}`; unknown fields are rejected (400), oversized bodies get 413, invalid batches 422.
- Protect discovery (`ListToolsHandler`) like execution: it reveals tool names and schemas.
- Authentication and per-caller scoping belong in middleware that runs before the handler. CORS (`blaze.CORS`, registered with `Wrap`) is not authentication.
- `Engine.Server` and `Listen` apply 5 s read-header, 30 s read, 60 s write and 120 s idle timeouts and a 64 KiB header limit. Use TLS before exposing a server beyond loopback.

## MCP

The MCP server calls the same executor, so everything above applies. In addition, `mcp.NewHTTPHandler` is stateless with single JSON responses (no long-lived streams), caps bodies at `MaxRequestBytes`, rejects DNS-rebinding `Host` headers on loopback and cross-origin browser requests, and authenticates nobody: wrap it in your authentication. Over stdio, standard output carries the protocol. Details: [MCP](mcp.md).

## Reporting a vulnerability

Please report security issues privately through [GitHub security advisories](https://github.com/dvictor357/blaze/security/advisories/new) rather than a public issue.
