# Anthropic tool conversions

Blaze provides pure conversions for Anthropic tool definitions and tool-use/result blocks. It does not call Claude, implement the Messages API, generate assistant responses, or report model token usage.

## Correct message direction

1. Your application sends `Tool.ToAnthropic()` definitions to Claude using its own provider client.
2. Claude's **assistant** message contains `tool_use` blocks. Retain that complete assistant message, including any text/thinking content, in the conversation.
3. Convert its current tool-use blocks with `adapter.AnthropicCalls` and execute those explicit calls.
4. `adapter.AnthropicToolResultMessage` builds the following **user** message containing correlated `tool_result` blocks.
5. Send the updated conversation back to Claude.

A tool result is not an assistant message. Do not put assistant tool-use blocks in a user request or replay older conversation calls.

## Conversion example

```go
package integration

import (
    "context"

    "github.com/dvictor357/blaze/adapter"
)

func ExecuteAnthropicTools(ctx context.Context, ex *adapter.Executor, current []adapter.AnthropicContentBlock) (adapter.AnthropicMessage, error) {
    calls, err := adapter.AnthropicCalls(current)
    if err != nil {
        return adapter.AnthropicMessage{}, err
    }
    if len(calls) == 0 {
        return adapter.AnthropicMessage{}, nil
    }
    results, err := ex.Execute(ctx, calls)
    if err != nil {
        return adapter.AnthropicMessage{}, err
    }
    return adapter.AnthropicToolResultMessage(results)
}
```

An empty return message means the current response contained no tool calls; do not append it. Your provider client owns conversion to/from its SDK types and preservation of the original assistant content. These helpers are not a replacement for the provider's full message schema.

`AnthropicCalls` reads only supplied `tool_use` blocks, ignores other block types, and requires a tool-use ID. Inputs are `json.RawMessage`, preserving large-integer JSON values. Missing input becomes `{}` during execution. The executor validates the full batch before running any call.

`AnthropicToolResults` returns one `tool_result` block per result. Each block carries the original `tool_use_id`; failures set `is_error:true` and encode structured error content. `AnthropicToolResultMessage` wraps those blocks in `role:"user"`.

## Definitions and validation

`Tool.ToAnthropic()` yields `AnthropicToolDef` with `name`, `description`, and object-shaped `input_schema`. `adapter.ListToolsHandler(ex)` exposes these definitions in its `anthropic` field.

Input schemas are descriptive model metadata, not generic runtime validation. Tool handlers must validate their domain input and authorization. Error messages are returned to callers/models; do not include secrets.

## HTTP and streaming

The [Blaze invocation API](../../adapter/README.md) accepts explicit `calls`, not Anthropic Messages API requests. Its `result`/`done` SSE events are **not** Anthropic model events. Assemble tool inputs from a provider stream before invoking them.

Conversions never search conversation history. Explicitly submitting a call again executes it again; sensitive tools need application-owned idempotency.

## Live SDK proof

The opt-in `TestLiveAnthropic` test in [integration/providers/live_test.go](../../integration/providers/live_test.go) uses the official `anthropic-sdk-go/v2` SDK. It forces an assistant tool-use block, executes it through Blaze, returns a correlated user tool-result message, and requires the final model reply to contain a confirmation token created only during execution. The original assistant message is preserved with the SDK's `ToParam()` conversion.

Set `ANTHROPIC_API_KEY` to a funded provider API key and `ANTHROPIC_MODEL` to an accessible Messages API model supporting named tool choice and a 256-token reply cap. From the repository root:

```sh
(
  cd integration/providers
  : "${ANTHROPIC_API_KEY:?set ANTHROPIC_API_KEY in your environment}"
  : "${ANTHROPIC_MODEL:?set an accessible ANTHROPIC_MODEL}"
  BLAZE_LIVE_PROVIDERS=1 go test -v -count=1 -run '^TestLiveAnthropic$' -timeout 2m ./...
)
```

This makes two billed API requests to `https://api.anthropic.com`, with no SDK retries and a 90-second context. It does not use Claude Code, subscription/OAuth credentials, or a coding agent. Missing configuration fails when live mode is enabled; skipped tests or successful compilation are not live-provider proof.

The manual [Live provider proof workflow](../../.github/workflows/providers.yml) runs both providers after both secrets and model IDs are configured. Normal CI never makes paid requests.

