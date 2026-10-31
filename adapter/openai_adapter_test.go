package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestToOpenAI(t *testing.T) {
	ex := mustExecutor(t, Config{}, echoTool())

	raw, err := json.Marshal(ex.Tools()[0].ToOpenAI())
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Type     string `json:"type"`
		Function struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "function" || got.Function.Name != "echo" || got.Function.Description != "Echo back the message" {
		t.Errorf("definition = %s", raw)
	}
	// The schema is a JSON object, not an escaped string.
	if got.Function.Parameters["type"] != "object" {
		t.Errorf("parameters = %v", got.Function.Parameters)
	}
}

func TestOpenAICalls(t *testing.T) {
	calls, err := OpenAICalls([]OpenAIToolCall{
		{ID: "call_a", Type: "function", Function: OpenAIFunctionCall{Name: "echo", Arguments: `{"message":"hi"}`}},
		{ID: "call_b", Function: OpenAIFunctionCall{Name: "echo", Arguments: "  "}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("got %d calls", len(calls))
	}
	if calls[0].ID != "call_a" || calls[0].Name != "echo" || string(calls[0].Input) != `{"message":"hi"}` {
		t.Errorf("call 0 = %+v", calls[0])
	}
	if calls[1].ID != "call_b" || string(calls[1].Input) != `{}` {
		t.Errorf("empty arguments must become {}: %+v", calls[1])
	}

	if _, err := OpenAICalls([]OpenAIToolCall{{ID: "x", Type: "custom", Function: OpenAIFunctionCall{Name: "echo"}}}); err == nil {
		t.Error("non-function tool call types must be rejected")
	}
}

func TestOpenAI_FullToolCycle(t *testing.T) {
	failing := NewTool("explode", "always fails", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		return nil, errors.New("said \"no\"\nwith a newline and a \\ backslash")
	})
	ex := mustExecutor(t, Config{}, echoTool(), failing)

	// The model's current assistant message asked for two tool calls.
	calls, err := OpenAICalls([]OpenAIToolCall{
		{ID: "call_1", Type: "function", Function: OpenAIFunctionCall{Name: "echo", Arguments: `{"message":"hello"}`}},
		{ID: "call_2", Type: "function", Function: OpenAIFunctionCall{Name: "explode", Arguments: `{}`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	results, err := ex.Execute(context.Background(), calls)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := OpenAIToolMessages(results)
	if err != nil {
		t.Fatal(err)
	}

	if len(msgs) != 2 {
		t.Fatalf("got %d messages", len(msgs))
	}
	for i, id := range []string{"call_1", "call_2"} {
		if msgs[i].Role != "tool" || msgs[i].ToolCallID != id {
			t.Errorf("message %d = %+v, want role tool and id %s", i, msgs[i], id)
		}
		if !json.Valid([]byte(msgs[i].Content)) {
			t.Errorf("message %d content is not valid JSON: %q", i, msgs[i].Content)
		}
	}
	if msgs[0].Content != `{"echoed":"hello"}` {
		t.Errorf("success content = %s", msgs[0].Content)
	}

	var failure struct {
		Error CallError `json:"error"`
	}
	if err := json.Unmarshal([]byte(msgs[1].Content), &failure); err != nil {
		t.Fatalf("error content: %v", err)
	}
	if failure.Error.Code != CodeHandler || failure.Error.Message != "said \"no\"\nwith a newline and a \\ backslash" {
		t.Errorf("error content must round-trip exactly, got %+v", failure.Error)
	}

	// The wire shape matches what OpenAI expects.
	wire, _ := json.Marshal(msgs[0])
	var shape map[string]string
	_ = json.Unmarshal(wire, &shape)
	if shape["role"] != "tool" || shape["tool_call_id"] != "call_1" || shape["content"] == "" {
		t.Errorf("wire message = %s", wire)
	}
}

func TestOpenAI_InvalidArgumentsAreRejectedBeforeAnythingRuns(t *testing.T) {
	n := 0
	ex := mustExecutor(t, Config{}, counterTool("count", &n))

	calls, err := OpenAICalls([]OpenAIToolCall{
		{ID: "c1", Function: OpenAIFunctionCall{Name: "count", Arguments: `{}`}},
		{ID: "c2", Function: OpenAIFunctionCall{Name: "count", Arguments: `{"truncated": `}}, // model emitted broken JSON
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ex.Execute(context.Background(), calls); err == nil || n != 0 {
		t.Errorf("err=%v, executions=%d; a broken member must reject the whole batch", err, n)
	}
}

func TestOpenAIToolMessages_RejectsInconsistentResults(t *testing.T) {
	bad := []Result{
		{ID: "1", OK: true}, // OK without output
		{ID: "2", OK: true, Output: json.RawMessage(`{`)}, // invalid JSON output
		{ID: "3"}, // neither OK nor error
	}
	for _, r := range bad {
		if _, err := OpenAIToolMessages([]Result{r}); err == nil {
			t.Errorf("result %+v was accepted", r)
		}
	}
}
