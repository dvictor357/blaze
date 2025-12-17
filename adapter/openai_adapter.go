package adapter

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dvictor357/blaze"
)

// ============================================================================
// OpenAI Types
// ============================================================================

// OpenAIToolDef represents an OpenAI tool definition
type OpenAIToolDef struct {
	Type     string            `json:"type"` // always "function"
	Function OpenAIFunctionDef `json:"function"`
}

// OpenAIFunctionDef represents the function details within a tool
type OpenAIFunctionDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"` // JSON Schema
}

// OpenAIMessage represents an OpenAI chat message
type OpenAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// OpenAIToolCall represents a tool call from the assistant
type OpenAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // "function"
	Function OpenAIFunctionCall `json:"function"`
}

// OpenAIFunctionCall represents the function call details
type OpenAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// OpenAIChatRequest represents an OpenAI chat completion request
type OpenAIChatRequest struct {
	Model    string          `json:"model"`
	Messages []OpenAIMessage `json:"messages"`
	Tools    []OpenAIToolDef `json:"tools,omitempty"`
	Stream   bool            `json:"stream,omitempty"`
}

// OpenAIChatResponse represents an OpenAI chat completion response
type OpenAIChatResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   OpenAIUsage    `json:"usage,omitempty"`
}

// OpenAIChoice represents a choice in the response
type OpenAIChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// OpenAIUsage represents token usage information
type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OpenAIStreamChunk represents a streaming response chunk
type OpenAIStreamChunk struct {
	ID      string               `json:"id"`
	Object  string               `json:"object"`
	Created int64                `json:"created"`
	Model   string               `json:"model"`
	Choices []OpenAIStreamChoice `json:"choices"`
}

// OpenAIStreamChoice represents a choice in a streaming chunk
type OpenAIStreamChoice struct {
	Index        int            `json:"index"`
	Delta        OpenAIDelta    `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

// OpenAIDelta represents the delta content in streaming
type OpenAIDelta struct {
	Role      string           `json:"role,omitempty"`
	Content   string           `json:"content,omitempty"`
	ToolCalls []OpenAIToolCall `json:"tool_calls,omitempty"`
}

// ============================================================================
// Tool Conversion Methods
// ============================================================================

// ToOpenAI converts a Tool to OpenAI tool definition format
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

// ToAnthropic converts a Tool to Anthropic tool definition format
func (t Tool) ToAnthropic() map[string]any {
	return map[string]any{
		"name":         t.Name,
		"description":  t.Description,
		"input_schema": t.InputSchema,
	}
}

// ============================================================================
// OpenAI Adapter
// ============================================================================

// OpenAIAdapter creates a Blaze handler that processes OpenAI-format requests
// and executes registered tools
func OpenAIAdapter(tools ...Tool) blaze.HandlerFunc {
	toolMap := make(map[string]Tool)
	for _, tool := range tools {
		toolMap[tool.Name] = tool
	}

	return func(ctx *blaze.Context) error {
		var req OpenAIChatRequest
		if err := ctx.BindJSON(&req); err != nil {
			return ctx.JSON(400, map[string]any{
				"error": map[string]any{
					"message": fmt.Sprintf("Invalid request: %v", err),
					"type":    "invalid_request_error",
				},
			})
		}

		if len(req.Messages) == 0 {
			return ctx.JSON(400, map[string]any{
				"error": map[string]any{
					"message": "Messages array is required",
					"type":    "invalid_request_error",
				},
			})
		}

		// Find tool calls in the last assistant message
		var toolCalls []OpenAIToolCall
		for i := len(req.Messages) - 1; i >= 0; i-- {
			msg := req.Messages[i]
			if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
				toolCalls = msg.ToolCalls
				break
			}
		}

		// If no tool calls found, return available tools info
		if len(toolCalls) == 0 {
			return handleNoToolCalls(ctx, req, tools)
		}

		// Execute each tool call
		toolResults := make([]OpenAIMessage, 0, len(toolCalls))
		for _, tc := range toolCalls {
			tool, exists := toolMap[tc.Function.Name]
			if !exists {
				toolResults = append(toolResults, OpenAIMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf(`{"error": "Tool '%s' not found"}`, tc.Function.Name),
				})
				continue
			}

			// Execute the tool handler
			result, err := tool.Handler(json.RawMessage(tc.Function.Arguments))
			if err != nil {
				toolResults = append(toolResults, OpenAIMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf(`{"error": "%v"}`, err),
				})
				continue
			}

			// Convert result to JSON string
			resultBytes, _ := json.Marshal(result)
			toolResults = append(toolResults, OpenAIMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    string(resultBytes),
			})
		}

		// Return response based on streaming preference
		if req.Stream {
			return streamOpenAIResponse(ctx, req.Model, toolResults)
		}
		return sendOpenAIResponse(ctx, req.Model, toolResults)
	}
}

// handleNoToolCalls returns a response when no tool calls are present
func handleNoToolCalls(ctx *blaze.Context, req OpenAIChatRequest, tools []Tool) error {
	// Build tool list for response
	toolDefs := make([]OpenAIToolDef, len(tools))
	for i, t := range tools {
		toolDefs[i] = t.ToOpenAI()
	}

	// Get last user message
	var lastUserContent string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			lastUserContent = req.Messages[i].Content
			break
		}
	}

	response := OpenAIChatResponse{
		ID:      generateID("chatcmpl"),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Message: OpenAIMessage{
					Role:    "assistant",
					Content: fmt.Sprintf("I have access to %d tools. To use them, include tool_calls in your request. Your message: %s", len(tools), lastUserContent),
				},
				FinishReason: "stop",
			},
		},
		Usage: OpenAIUsage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}

	return ctx.JSON(200, response)
}

// sendOpenAIResponse sends a non-streaming response
func sendOpenAIResponse(ctx *blaze.Context, model string, toolResults []OpenAIMessage) error {
	// Combine tool results into content
	var combinedContent string
	for _, result := range toolResults {
		combinedContent += result.Content + "\n"
	}

	response := OpenAIChatResponse{
		ID:      generateID("chatcmpl"),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Message: OpenAIMessage{
					Role:    "assistant",
					Content: combinedContent,
				},
				FinishReason: "stop",
			},
		},
		Usage: OpenAIUsage{
			PromptTokens:     10,
			CompletionTokens: len(combinedContent) / 4,
			TotalTokens:      10 + len(combinedContent)/4,
		},
	}

	return ctx.JSON(200, response)
}

// streamOpenAIResponse sends a streaming SSE response
func streamOpenAIResponse(ctx *blaze.Context, model string, toolResults []OpenAIMessage) error {
	ch := make(chan any)

	go func() {
		defer close(ch)

		id := generateID("chatcmpl")
		created := time.Now().Unix()

		// Send initial chunk with role
		ch <- OpenAIStreamChunk{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []OpenAIStreamChoice{
				{
					Index: 0,
					Delta: OpenAIDelta{
						Role: "assistant",
					},
					FinishReason: nil,
				},
			},
		}

		// Send content chunks for each tool result
		for _, result := range toolResults {
			ch <- OpenAIStreamChunk{
				ID:      id,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model,
				Choices: []OpenAIStreamChoice{
					{
						Index: 0,
						Delta: OpenAIDelta{
							Content: result.Content + "\n",
						},
						FinishReason: nil,
					},
				},
			}
		}

		// Send final chunk with finish_reason
		stopReason := "stop"
		ch <- OpenAIStreamChunk{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []OpenAIStreamChoice{
				{
					Index:        0,
					Delta:        OpenAIDelta{},
					FinishReason: &stopReason,
				},
			},
		}
	}()

	return ctx.StreamJSON(ch)
}

// ============================================================================
// ListTools Handler
// ============================================================================

// ToolListResponse represents the response from ListTools endpoint
type ToolListResponse struct {
	OpenAI    []OpenAIToolDef  `json:"openai"`
	Anthropic []map[string]any `json:"anthropic"`
	Count     int              `json:"count"`
}

// ListToolsHandler creates a handler that returns available tools in multiple formats
func ListToolsHandler(tools ...Tool) blaze.HandlerFunc {
	return func(ctx *blaze.Context) error {
		openaiTools := make([]OpenAIToolDef, len(tools))
		anthropicTools := make([]map[string]any, len(tools))

		for i, t := range tools {
			openaiTools[i] = t.ToOpenAI()
			anthropicTools[i] = t.ToAnthropic()
		}

		return ctx.JSON(200, ToolListResponse{
			OpenAI:    openaiTools,
			Anthropic: anthropicTools,
			Count:     len(tools),
		})
	}
}

// ============================================================================
// Helpers
// ============================================================================

// generateID creates a unique ID with the given prefix
func generateID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
