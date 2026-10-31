# OpenAI tool conversions

Blaze provides pure conversions for OpenAI Chat Completions function-tool definitions, current tool calls, and tool-result messages. It does not implement Chat Completions, call a model, generate assistant text, or estimate token usage.

## Model interaction

Your application performs this cycle:

1. Convert registered tools with `Tool.ToOpenAI()` and send those definitions in your model request.
2. Keep the model's assistant message in the conversation.
3. Pass only that response's **current** `tool_calls` to `adapter.OpenAICalls`.
4. Execute the resulting calls with your executor.
5. Convert results with `adapter.OpenAIToolMessages` and append them after the assistant message.
6. Send the updated conversation back to your model.

Do not resubmit completed calls from older assistant messages. Conversions never inspect history, and executing an explicit call again repeats its side effects.

## Conversion example

The following function accepts current, non-streaming/fully assembled tool calls, not conversation history:

```go
package integration

import (
    "context"

    "github.com/dvictor357/blaze/adapter"
)

func ExecuteOpenAITools(ctx context.Context, ex *adapter.Executor, current []adapter.OpenAIToolCall) ([]adapter.OpenAIToolMessage, error) {
    calls, err := adapter.OpenAICalls(current)
    if err != nil {
        return nil, err
    }
    if len(calls) == 0 {
        return nil, nil
    }
    results, err := ex.Execute(ctx, calls)
    if err != nil {
        return nil, err
    }
    return adapter.OpenAIToolMessages(results)
}
```

`OpenAIToolCall` holds `ID`, `Type`, and `Function` (`Name`, `Arguments`). Arguments are a JSON string; conversion preserves the JSON bytes, including large integers. Empty arguments become `{}`. Unsupported call types fail conversion. The executor validates known names, unique IDs, object-shaped inputs, and batch budgets before execution.

`OpenAIToolMessages` emits one message per result:

- `role: "tool"`
- `tool_call_id`: the original call ID
- `content`: JSON text of the successful output, or an encoded `{"error":{"code":...,"message":...}}`

Call correlation is preserved; outputs are not combined into an assistant string. Handler error content is correctly escaped. Treat returned error messages as public to your model/caller and avoid putting secrets in them.

## Definitions and discovery

`Tool.ToOpenAI()` yields `OpenAIToolDef{Type:"function", Function:...}`; `Function.Parameters` is a schema object, not an encoded string. `adapter.ListToolsHandler(ex)` exposes all definitions in its `openai` field. Protect the discovery route like the execution route.

Before a handler runs, the executor validates the arguments against the tool's schema. A violation becomes a correlated `invalid_arguments` tool message listing each problem with its JSON path (for example `$.units: must be one of ["metric","imperial"]`), so the model can correct its call; the handler is not called. Handlers still enforce authorization and domain rules a schema cannot express. See [schema validation](../../adapter/README.md#schema-validation) for the supported keywords.

## HTTP and streaming

An application can instead forward converted calls to Blaze's [explicit invocation API](../../adapter/README.md). That request uses `calls`, not `messages`, `model`, or `tools`. Its SSE stream is Blaze's `result`/`done` protocol, **not** OpenAI model SSE. If your model streams arguments, assemble each current tool call in your provider client before invoking it.

Blaze does not deduplicate requests or provide exactly-once execution. Use application-level idempotency for operations that cannot safely repeat.

## Live SDK proof

The opt-in `TestLiveOpenAI` test in [integration/providers/live_test.go](../../integration/providers/live_test.go) uses the official `openai-go/v3` SDK. It forces a function call, executes it through Blaze, returns the correlated tool message through the SDK, and requires the final model reply to contain a confirmation token created only during tool execution.

Set `OPENAI_API_KEY` to a funded provider API key and `OPENAI_MODEL` to a model ID accessible to that key. The model must support Chat Completions named function-tool choice, disabled parallel calls, and a 256-token completion cap. From the repository root:

```sh
(
  cd integration/providers
  : "${OPENAI_API_KEY:?set OPENAI_API_KEY in your environment}"
  : "${OPENAI_MODEL:?set an accessible OPENAI_MODEL}"
  BLAZE_LIVE_PROVIDERS=1 go test -v -count=1 -run '^TestLiveOpenAI$' -timeout 2m ./...
)
```

This makes two billed API requests to `https://api.openai.com/v1`, with no SDK retries and a 90-second context. Missing configuration fails when live mode is enabled. Without explicit live opt-in, the test skips; compilation or a skip is not proof that a real provider round trip passed.

For GitHub execution, use the manual [Live provider proof workflow](../../.github/workflows/providers.yml) after configuring both provider secrets and selecting model IDs. Normal CI never runs paid requests. No coding-agent login is used by the harness.

