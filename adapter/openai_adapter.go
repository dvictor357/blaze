package adapter

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file converts between Blaze tools and OpenAI function-calling wire
// shapes. Blaze never talks to a model: the application sends the tool
// definitions to its model, passes the model's CURRENT tool calls to
// OpenAICalls, executes them, and appends OpenAIToolMessages to the
// conversation. Conversion works on exactly the calls it is given and never
// reads conversation history.

// OpenAIToolDef is an entry of the "tools" array of a chat completion request.
type OpenAIToolDef struct {
	Type     string            `json:"type"` // always "function"
	Function OpenAIFunctionDef `json:"function"`
}

// OpenAIFunctionDef describes a function tool.
type OpenAIFunctionDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"` // JSON Schema object
}

// OpenAIToolCall is a tool call produced by the model in an assistant message.
type OpenAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // "function"
	Function OpenAIFunctionCall `json:"function"`
}

// OpenAIFunctionCall carries the function name and its JSON-string arguments.
type OpenAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// OpenAIToolMessage is the "tool" role message that answers one tool call.
type OpenAIToolMessage struct {
	Role       string `json:"role"` // always "tool"
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
}

// ToOpenAI converts a Tool to an OpenAI tool definition.
func (t Tool) ToOpenAI() OpenAIToolDef {
	return OpenAIToolDef{
		Type: "function",
		Function: OpenAIFunctionDef{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		},
	}
}

// OpenAICalls converts the tool calls of the model's current assistant
// message into Calls, preserving call IDs. Empty arguments (which models send
// for parameterless functions) become {}. Anything else is passed through
// unchanged and validated by the Executor before any call runs.
func OpenAICalls(calls []OpenAIToolCall) ([]Call, error) {
	out := make([]Call, len(calls))
	for i, tc := range calls {
		if tc.Type != "" && tc.Type != "function" {
			return nil, fmt.Errorf("adapter: tool call %d (%q): unsupported type %q", i, tc.ID, tc.Type)
		}
		args := strings.TrimSpace(tc.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		out[i] = Call{ID: tc.ID, Name: tc.Function.Name, Input: json.RawMessage(args)}
	}
	return out, nil
}

// OpenAIToolMessages converts results into "tool" role messages, one per
// result, in order, keyed by tool_call_id. Successful content is the tool's
// JSON output; failures are {"error":{"code":...,"message":...}}. OpenAI has
// no error flag on tool messages, so errors are conveyed in the content.
func OpenAIToolMessages(results []Result) ([]OpenAIToolMessage, error) {
	out := make([]OpenAIToolMessage, len(results))
	for i, r := range results {
		content, err := resultContent(r)
		if err != nil {
			return nil, err
		}
		out[i] = OpenAIToolMessage{Role: "tool", ToolCallID: r.ID, Content: content}
	}
	return out, nil
}

// resultContent renders a result as the JSON text a model receives. All JSON
// is produced by the encoder, so messages are always escaped correctly.
func resultContent(r Result) (string, error) {
	if r.OK {
		if len(r.Output) == 0 {
			return "", fmt.Errorf("adapter: result %q is OK but has no output", r.ID)
		}
		if !json.Valid(r.Output) {
			return "", fmt.Errorf("adapter: result %q has invalid JSON output", r.ID)
		}
		return string(r.Output), nil
	}
	if r.Error == nil {
		return "", fmt.Errorf("adapter: result %q is neither OK nor an error", r.ID)
	}
	b, err := json.Marshal(struct {
		Error *CallError `json:"error"`
	}{r.Error})
	if err != nil {
		return "", fmt.Errorf("adapter: encode error for result %q: %w", r.ID, err)
	}
	return string(b), nil
}
