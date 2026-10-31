package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestToAnthropic(t *testing.T) {
	ex := mustExecutor(t, Config{}, echoTool())

	raw, err := json.Marshal(ex.Tools()[0].ToAnthropic())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	schema, _ := got["input_schema"].(map[string]any)
	if got["name"] != "echo" || got["description"] != "Echo back the message" || schema["type"] != "object" {
		t.Errorf("definition = %s", raw)
	}
}

func TestAnthropicCalls_TakesToolUseFromAssistantContent(t *testing.T) {
	assistant := []AnthropicContentBlock{
		{Type: "text", Text: "Let me check."},
		{Type: "tool_use", ID: "toolu_1", Name: "echo", Input: json.RawMessage(`{"message":"hi"}`)},
		{Type: "tool_use", ID: "toolu_2", Name: "echo"}, // no input
	}
	calls, err := AnthropicCalls(assistant)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2 (text blocks are ignored)", len(calls))
	}
	if calls[0].ID != "toolu_1" || calls[0].Name != "echo" || string(calls[0].Input) != `{"message":"hi"}` {
		t.Errorf("call 0 = %+v", calls[0])
	}
	if calls[1].ID != "toolu_2" || len(calls[1].Input) != 0 {
		t.Errorf("call 1 = %+v", calls[1])
	}

	if calls, err := AnthropicCalls([]AnthropicContentBlock{{Type: "text", Text: "just text"}}); err != nil || len(calls) != 0 {
		t.Errorf("text-only content = %v, %v; want no calls", calls, err)
	}
	if _, err := AnthropicCalls([]AnthropicContentBlock{{Type: "tool_use", Name: "echo"}}); err == nil {
		t.Error("a tool_use without an id cannot be answered and must be rejected")
	}
}

func TestAnthropicCalls_PreservesInputBytes(t *testing.T) {
	// Decoding the model's tool_use block and converting it must not
	// reformat numbers or reorder keys.
	var block AnthropicContentBlock
	src := `{"type":"tool_use","id":"toolu_9","name":"echo","input":{"b":12345678901234567890,"a":1.50}}`
	if err := json.Unmarshal([]byte(src), &block); err != nil {
		t.Fatal(err)
	}
	calls, err := AnthropicCalls([]AnthropicContentBlock{block})
	if err != nil {
		t.Fatal(err)
	}
	if string(calls[0].Input) != `{"b":12345678901234567890,"a":1.50}` {
		t.Errorf("input = %s", calls[0].Input)
	}
}

func TestAnthropic_FullToolCycle(t *testing.T) {
	failing := NewTool("explode", "always fails", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		return nil, errors.New(`said "no"`)
	})
	ex := mustExecutor(t, Config{}, echoTool(), failing)

	calls, err := AnthropicCalls([]AnthropicContentBlock{
		{Type: "tool_use", ID: "toolu_ok", Name: "echo", Input: json.RawMessage(`{"message":"hello"}`)},
		{Type: "tool_use", ID: "toolu_bad", Name: "explode", Input: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	results, err := ex.Execute(context.Background(), calls)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := AnthropicToolResultMessage(results)
	if err != nil {
		t.Fatal(err)
	}

	// Tool results travel in a USER message.
	if msg.Role != "user" || len(msg.Content) != 2 {
		t.Fatalf("message = %+v", msg)
	}
	ok, bad := msg.Content[0], msg.Content[1]
	if ok.Type != "tool_result" || ok.ToolUseID != "toolu_ok" || ok.IsError || ok.Content != `{"echoed":"hello"}` {
		t.Errorf("success block = %+v", ok)
	}
	if bad.Type != "tool_result" || bad.ToolUseID != "toolu_bad" || !bad.IsError {
		t.Errorf("failure block = %+v", bad)
	}
	var failure struct {
		Error CallError `json:"error"`
	}
	if err := json.Unmarshal([]byte(bad.Content), &failure); err != nil || failure.Error.Message != `said "no"` {
		t.Errorf("failure content = %q (%v)", bad.Content, err)
	}

	// Wire shape: is_error only appears on failures.
	wire, _ := json.Marshal(msg)
	if strings.Count(string(wire), `"is_error":true`) != 1 || strings.Contains(string(wire), `"is_error":false`) {
		t.Errorf("wire = %s", wire)
	}
}

func TestAnthropicToolResults_RejectInconsistentResults(t *testing.T) {
	for _, r := range []Result{{ID: "1", OK: true}, {ID: "2"}} {
		if _, err := AnthropicToolResults([]Result{r}); err == nil {
			t.Errorf("result %+v was accepted", r)
		}
	}
}
