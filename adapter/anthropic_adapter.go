package adapter

import (
	"encoding/json"
	"fmt"
)

// This file converts between Blaze tools and Anthropic tool-use wire shapes.
// Blaze never talks to a model. In Anthropic's protocol the model emits
// tool_use blocks in an ASSISTANT message, and the application answers with
// tool_result blocks in the next USER message. AnthropicCalls takes the
// current assistant content blocks; AnthropicToolResultMessage builds the
// reply. Conversion works on exactly what it is given and never reads history.

// AnthropicToolDef is an entry of the "tools" array of a messages request.
type AnthropicToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"` // JSON Schema object
}

// AnthropicContentBlock is a content block: "tool_use" (from the assistant)
// or "tool_result" (from the user). Other block types are ignored by
// AnthropicCalls.
type AnthropicContentBlock struct {
	Type string `json:"type"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// text
	Text string `json:"text,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// AnthropicMessage is a message whose content is a list of blocks.
type AnthropicMessage struct {
	Role    string                  `json:"role"`
	Content []AnthropicContentBlock `json:"content"`
}

// ToAnthropic converts a Tool to an Anthropic tool definition.
func (t Tool) ToAnthropic() AnthropicToolDef {
	return AnthropicToolDef{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: t.InputSchema,
	}
}

// AnthropicCalls converts the tool_use blocks of the model's current
// assistant message into Calls, preserving tool_use IDs. Other block types
// (text, thinking...) are ignored. A missing input becomes {}; anything else is
// validated by the Executor before any call runs.
func AnthropicCalls(content []AnthropicContentBlock) ([]Call, error) {
	var out []Call
	for i, b := range content {
		if b.Type != "tool_use" {
			continue
		}
		if b.ID == "" {
			return nil, fmt.Errorf("adapter: content block %d: tool_use without id", i)
		}
		out = append(out, Call{ID: b.ID, Name: b.Name, Input: b.Input})
	}
	return out, nil
}

// AnthropicToolResults converts results into tool_result blocks, one per
// result, in order, keyed by tool_use_id. Failures set is_error and carry
// {"error":{"code":...,"message":...}} as content.
func AnthropicToolResults(results []Result) ([]AnthropicContentBlock, error) {
	out := make([]AnthropicContentBlock, len(results))
	for i, r := range results {
		content, err := resultContent(r)
		if err != nil {
			return nil, err
		}
		out[i] = AnthropicContentBlock{
			Type:      "tool_result",
			ToolUseID: r.ID,
			Content:   content,
			IsError:   !r.OK,
		}
	}
	return out, nil
}

// AnthropicToolResultMessage wraps AnthropicToolResults in the user message
// that must follow the assistant message containing the tool_use blocks.
func AnthropicToolResultMessage(results []Result) (AnthropicMessage, error) {
	blocks, err := AnthropicToolResults(results)
	if err != nil {
		return AnthropicMessage{}, err
	}
	return AnthropicMessage{Role: "user", Content: blocks}, nil
}
