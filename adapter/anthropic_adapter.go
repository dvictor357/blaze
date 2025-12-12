package adapter

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dvictor357/blaze"
)

type Tool struct {
	Name        string
	Description string
	InputSchema any
	Handler     func(json.RawMessage) (any, error)
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ContentBlock struct {
	Type      string         `json:"type"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Text      string         `json:"text,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
}

func AnthropicAdapter(tools ...Tool) blaze.HandlerFunc {
	toolMap := make(map[string]Tool)
	for _, tool := range tools {
		toolMap[tool.Name] = tool
	}

	return func(ctx *blaze.Context) error {
		// Parse incoming request
		var req struct {
			Model     string           `json:"model"`
			Messages  []Message        `json:"messages"`
			MaxTokens int              `json:"max_tokens"`
			Tools     []map[string]any `json:"tools"`
		}

		if err := ctx.BindJSON(&req); err != nil {
			return err
		}

		lastMessage := req.Messages[len(req.Messages)-1]
		if lastMessage.Role != "user" {
			return errors.New("last message must be from user")
		}

		var content []ContentBlock
		if err := json.Unmarshal([]byte(lastMessage.Content), &content); err != nil {
			content = []ContentBlock{{Type: "text", Text: lastMessage.Content}}
		}

		responses := []ContentBlock{}
		for _, block := range content {
			if block.Type == "tool_use" {
				tool, exists := toolMap[block.Name]
				if !exists {
					responses = append(responses, ContentBlock{
						Type:      "tool_result",
						ToolUseID: block.ID,
						Content:   "Tool not found",
					})
					continue
				}

				inputBytes, _ := json.Marshal(block.Input)
				result, err := tool.Handler(inputBytes)
				if err != nil {
					responses = append(responses, ContentBlock{
						Type:      "tool_result",
						ToolUseID: block.ID,
						Content:   fmt.Sprintf("Error: %v", err),
					})
					continue
				}

				resultStr := toJSON(result)
				responses = append(responses, ContentBlock{
					Type:      "tool_result",
					ToolUseID: block.ID,
					Content:   resultStr,
				})
			}
		}

		// Send streaming response
		return ctx.StreamJSON(streamResponse(responses))
	}
}

func streamResponse(blocks []ContentBlock) <-chan any {
	ch := make(chan any)
	go func() {
		defer close(ch)
		ch <- map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            "msg_1",
				"role":          "assistant",
				"model":         "claude-3-5-sonnet-20241022",
				"stop_sequence": nil,
			},
		}
		ch <- map[string]any{
			"type":          "content_block_start",
			"index":         0,
			"content_block": map[string]any{"type": "text", "text": "Processing tools..."},
		}
		for i, block := range blocks {
			ch <- map[string]any{
				"type":  "content_block_delta",
				"index": i,
				"delta": map[string]any{"type": block.Type, "text": block.Content},
			}
		}
		ch <- map[string]any{
			"type":        "message_stop",
			"stop_reason": "end_turn",
		}
	}()
	return ch
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// NewTool creates a new Tool with the given parameters
func NewTool(name, desc string, schema any, handler func(json.RawMessage) (any, error)) Tool {
	return Tool{Name: name, Description: desc, InputSchema: schema, Handler: handler}
}
