package adapter

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dvictor357/blaze"
)

// ============================================================================
// Shared Types (used by all adapters)
// ============================================================================

// Tool represents a callable function that can be registered with an adapter
type Tool struct {
	Name        string
	Description string
	InputSchema any
	Handler     func(json.RawMessage) (any, error)
}

// NewTool creates a new Tool with the given parameters
func NewTool(name, desc string, schema any, handler func(json.RawMessage) (any, error)) Tool {
	return Tool{Name: name, Description: desc, InputSchema: schema, Handler: handler}
}

// ============================================================================
// Anthropic Types
// ============================================================================

// AnthropicMessage represents an Anthropic chat message
type AnthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // Can be string or []ContentBlock
}

// AnthropicContentBlock represents a content block in Anthropic's format
type AnthropicContentBlock struct {
	Type      string         `json:"type"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Text      string         `json:"text,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
}

// AnthropicChatRequest represents an Anthropic chat completion request
type AnthropicChatRequest struct {
	Model     string             `json:"model"`
	Messages  []AnthropicMessage `json:"messages"`
	MaxTokens int                `json:"max_tokens,omitempty"`
	Tools     []map[string]any   `json:"tools,omitempty"`
	Stream    bool               `json:"stream,omitempty"`
}

// AnthropicChatResponse represents an Anthropic chat completion response
type AnthropicChatResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Model        string                  `json:"model"`
	Content      []AnthropicContentBlock `json:"content"`
	StopReason   string                  `json:"stop_reason"`
	StopSequence *string                 `json:"stop_sequence"`
	Usage        AnthropicUsage          `json:"usage"`
}

// AnthropicUsage represents token usage information
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicStreamEvent represents a streaming event
type AnthropicStreamEvent struct {
	Type         string         `json:"type"`
	Index        int            `json:"index,omitempty"`
	Message      map[string]any `json:"message,omitempty"`
	ContentBlock map[string]any `json:"content_block,omitempty"`
	Delta        map[string]any `json:"delta,omitempty"`
	StopReason   string         `json:"stop_reason,omitempty"`
}

// ============================================================================
// Anthropic Adapter
// ============================================================================

// AnthropicAdapter creates a Blaze handler that processes Anthropic/Claude-format
// requests and executes registered tools
func AnthropicAdapter(tools ...Tool) blaze.HandlerFunc {
	toolMap := make(map[string]Tool)
	for _, tool := range tools {
		toolMap[tool.Name] = tool
	}

	return func(ctx *blaze.Context) error {
		var req AnthropicChatRequest
		if err := ctx.BindJSON(&req); err != nil {
			return ctx.JSON(400, map[string]any{
				"type": "error",
				"error": map[string]any{
					"type":    "invalid_request_error",
					"message": fmt.Sprintf("Invalid request: %v", err),
				},
			})
		}

		if len(req.Messages) == 0 {
			return ctx.JSON(400, map[string]any{
				"type": "error",
				"error": map[string]any{
					"type":    "invalid_request_error",
					"message": "Messages array is required",
				},
			})
		}

		// Get the last message
		lastMessage := req.Messages[len(req.Messages)-1]
		if lastMessage.Role != "user" {
			return ctx.JSON(400, map[string]any{
				"type": "error",
				"error": map[string]any{
					"type":    "invalid_request_error",
					"message": "Last message must be from user",
				},
			})
		}

		// Parse content blocks from the message
		contentBlocks := parseContentBlocks(lastMessage.Content)

		// Find and execute tool_use blocks
		var toolResults []AnthropicContentBlock
		hasToolUse := false

		for _, block := range contentBlocks {
			if block.Type == "tool_use" {
				hasToolUse = true
				result := executeToolBlock(block, toolMap)
				toolResults = append(toolResults, result)
			}
		}

		// If no tool_use blocks, return info about available tools
		if !hasToolUse {
			return handleNoToolUse(ctx, req, tools)
		}

		// Return response based on streaming preference
		if req.Stream {
			return streamAnthropicResponse(ctx, req.Model, toolResults)
		}
		return sendAnthropicResponse(ctx, req.Model, toolResults)
	}
}

// parseContentBlocks parses the content field which can be string or []ContentBlock
func parseContentBlocks(content any) []AnthropicContentBlock {
	// Try as string first
	if str, ok := content.(string); ok {
		return []AnthropicContentBlock{{Type: "text", Text: str}}
	}

	// Try as JSON array
	contentBytes, err := json.Marshal(content)
	if err != nil {
		return []AnthropicContentBlock{{Type: "text", Text: fmt.Sprintf("%v", content)}}
	}

	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(contentBytes, &blocks); err != nil {
		return []AnthropicContentBlock{{Type: "text", Text: string(contentBytes)}}
	}

	return blocks
}

// executeToolBlock executes a single tool_use block and returns the result
func executeToolBlock(block AnthropicContentBlock, toolMap map[string]Tool) AnthropicContentBlock {
	tool, exists := toolMap[block.Name]
	if !exists {
		return AnthropicContentBlock{
			Type:      "tool_result",
			ToolUseID: block.ID,
			Content:   fmt.Sprintf(`{"error": "Tool '%s' not found"}`, block.Name),
		}
	}

	// Execute the tool handler
	inputBytes, _ := json.Marshal(block.Input)
	result, err := tool.Handler(inputBytes)
	if err != nil {
		return AnthropicContentBlock{
			Type:      "tool_result",
			ToolUseID: block.ID,
			Content:   fmt.Sprintf(`{"error": "%v"}`, err),
		}
	}

	resultBytes, _ := json.Marshal(result)
	return AnthropicContentBlock{
		Type:      "tool_result",
		ToolUseID: block.ID,
		Content:   string(resultBytes),
	}
}

// handleNoToolUse returns a response when no tool_use blocks are present
func handleNoToolUse(ctx *blaze.Context, req AnthropicChatRequest, tools []Tool) error {
	// Get text from last user message
	lastMessage := req.Messages[len(req.Messages)-1]
	var userText string
	if str, ok := lastMessage.Content.(string); ok {
		userText = str
	}

	response := AnthropicChatResponse{
		ID:    generateAnthropicID("msg"),
		Type:  "message",
		Role:  "assistant",
		Model: req.Model,
		Content: []AnthropicContentBlock{
			{
				Type: "text",
				Text: fmt.Sprintf("I have access to %d tools. To use them, include tool_use blocks in your request. Your message: %s", len(tools), userText),
			},
		},
		StopReason:   "end_turn",
		StopSequence: nil,
		Usage: AnthropicUsage{
			InputTokens:  10,
			OutputTokens: 20,
		},
	}

	return ctx.JSON(200, response)
}

// sendAnthropicResponse sends a non-streaming response
func sendAnthropicResponse(ctx *blaze.Context, model string, toolResults []AnthropicContentBlock) error {
	response := AnthropicChatResponse{
		ID:           generateAnthropicID("msg"),
		Type:         "message",
		Role:         "assistant",
		Model:        model,
		Content:      toolResults,
		StopReason:   "end_turn",
		StopSequence: nil,
		Usage: AnthropicUsage{
			InputTokens:  10,
			OutputTokens: len(toolResults) * 20,
		},
	}

	return ctx.JSON(200, response)
}

// streamAnthropicResponse sends a streaming SSE response
func streamAnthropicResponse(ctx *blaze.Context, model string, toolResults []AnthropicContentBlock) error {
	ch := make(chan any)

	go func() {
		defer close(ch)

		msgID := generateAnthropicID("msg")

		// message_start event
		ch <- AnthropicStreamEvent{
			Type: "message_start",
			Message: map[string]any{
				"id":            msgID,
				"type":          "message",
				"role":          "assistant",
				"model":         model,
				"stop_sequence": nil,
			},
		}

		// content_block_start for processing message
		ch <- AnthropicStreamEvent{
			Type:  "content_block_start",
			Index: 0,
			ContentBlock: map[string]any{
				"type": "text",
				"text": "Processing tools...",
			},
		}

		// Send each tool result as a delta
		for i, result := range toolResults {
			ch <- AnthropicStreamEvent{
				Type:  "content_block_delta",
				Index: i,
				Delta: map[string]any{
					"type": result.Type,
					"text": result.Content,
				},
			}
		}

		// message_stop event
		ch <- AnthropicStreamEvent{
			Type:       "message_stop",
			StopReason: "end_turn",
		}
	}()

	return ctx.StreamJSON(ch)
}

// ============================================================================
// Helpers
// ============================================================================

// generateAnthropicID creates a unique ID with the given prefix for Anthropic format
func generateAnthropicID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

// toJSON converts a value to JSON string (kept for backward compatibility)
func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Legacy type aliases for backward compatibility
type Message = AnthropicMessage
type ContentBlock = AnthropicContentBlock
