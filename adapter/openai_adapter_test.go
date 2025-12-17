package adapter

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dvictor357/blaze"
)

// TestOpenAIAdapter_ToolExecution tests that tool calls are executed correctly
func TestOpenAIAdapter_ToolExecution(t *testing.T) {
	// Create a simple echo tool
	echoTool := NewTool(
		"echo",
		"Echo back the input",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "Message to echo",
				},
			},
			"required": []string{"message"},
		},
		func(input json.RawMessage) (any, error) {
			var data struct {
				Message string `json:"message"`
			}
			json.Unmarshal(input, &data)
			return map[string]any{"echoed": data.Message}, nil
		},
	)

	// Create Blaze engine with OpenAI adapter
	e := blaze.New()
	e.POST("/openai", OpenAIAdapter(echoTool))

	// Create request with tool call
	reqBody := OpenAIChatRequest{
		Model: "gpt-4",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "Echo hello"},
			{
				Role: "assistant",
				ToolCalls: []OpenAIToolCall{
					{
						ID:   "call_123",
						Type: "function",
						Function: OpenAIFunctionCall{
							Name:      "echo",
							Arguments: `{"message": "hello world"}`,
						},
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/openai", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var resp OpenAIChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if len(resp.Choices) != 1 {
		t.Fatalf("Expected 1 choice, got %d", len(resp.Choices))
	}

	if !strings.Contains(resp.Choices[0].Message.Content, "hello world") {
		t.Errorf("Expected content to contain 'hello world', got: %s", resp.Choices[0].Message.Content)
	}
}

// TestOpenAIAdapter_NoToolCalls tests response when no tool calls are present
func TestOpenAIAdapter_NoToolCalls(t *testing.T) {
	echoTool := NewTool("echo", "Echo back the input", nil, nil)

	e := blaze.New()
	e.POST("/openai", OpenAIAdapter(echoTool))

	reqBody := OpenAIChatRequest{
		Model: "gpt-4",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/openai", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var resp OpenAIChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if len(resp.Choices) != 1 {
		t.Fatalf("Expected 1 choice, got %d", len(resp.Choices))
	}

	if !strings.Contains(resp.Choices[0].Message.Content, "1 tools") {
		t.Errorf("Expected content to mention available tools, got: %s", resp.Choices[0].Message.Content)
	}
}

// TestOpenAIAdapter_ToolNotFound tests error handling for unknown tools
func TestOpenAIAdapter_ToolNotFound(t *testing.T) {
	echoTool := NewTool("echo", "Echo back the input", nil, nil)

	e := blaze.New()
	e.POST("/openai", OpenAIAdapter(echoTool))

	reqBody := OpenAIChatRequest{
		Model: "gpt-4",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "Call unknown tool"},
			{
				Role: "assistant",
				ToolCalls: []OpenAIToolCall{
					{
						ID:   "call_456",
						Type: "function",
						Function: OpenAIFunctionCall{
							Name:      "unknown_tool",
							Arguments: `{}`,
						},
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/openai", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var resp OpenAIChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if !strings.Contains(resp.Choices[0].Message.Content, "not found") {
		t.Errorf("Expected error message about tool not found, got: %s", resp.Choices[0].Message.Content)
	}
}

// TestOpenAIAdapter_InvalidRequest tests error handling for invalid requests
func TestOpenAIAdapter_InvalidRequest(t *testing.T) {
	e := blaze.New()
	e.POST("/openai", OpenAIAdapter())

	req := httptest.NewRequest(http.MethodPost, "/openai", strings.NewReader("invalid json"))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rec.Code)
	}
}

// TestOpenAIAdapter_EmptyMessages tests error handling for empty messages
func TestOpenAIAdapter_EmptyMessages(t *testing.T) {
	e := blaze.New()
	e.POST("/openai", OpenAIAdapter())

	reqBody := OpenAIChatRequest{
		Model:    "gpt-4",
		Messages: []OpenAIMessage{},
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/openai", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rec.Code)
	}
}

// TestListToolsHandler tests the ListTools endpoint
func TestListToolsHandler(t *testing.T) {
	tools := []Tool{
		NewTool("tool1", "First tool", map[string]any{"type": "object"}, nil),
		NewTool("tool2", "Second tool", map[string]any{"type": "object"}, nil),
	}

	e := blaze.New()
	e.GET("/tools", ListToolsHandler(tools...))

	req := httptest.NewRequest(http.MethodGet, "/tools", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var resp ToolListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if resp.Count != 2 {
		t.Errorf("Expected count 2, got %d", resp.Count)
	}

	if len(resp.OpenAI) != 2 {
		t.Errorf("Expected 2 OpenAI tools, got %d", len(resp.OpenAI))
	}

	if len(resp.Anthropic) != 2 {
		t.Errorf("Expected 2 Anthropic tools, got %d", len(resp.Anthropic))
	}

	// Verify OpenAI format
	if resp.OpenAI[0].Type != "function" {
		t.Errorf("Expected OpenAI tool type 'function', got '%s'", resp.OpenAI[0].Type)
	}

	if resp.OpenAI[0].Function.Name != "tool1" {
		t.Errorf("Expected tool name 'tool1', got '%s'", resp.OpenAI[0].Function.Name)
	}

	// Verify Anthropic format
	if resp.Anthropic[0]["name"] != "tool1" {
		t.Errorf("Expected Anthropic tool name 'tool1', got '%v'", resp.Anthropic[0]["name"])
	}
}

// TestToolToOpenAI tests the ToOpenAI conversion method
func TestToolToOpenAI(t *testing.T) {
	tool := NewTool(
		"test_tool",
		"A test tool",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"param1": map[string]any{"type": "string"},
			},
		},
		nil,
	)

	openaiDef := tool.ToOpenAI()

	if openaiDef.Type != "function" {
		t.Errorf("Expected type 'function', got '%s'", openaiDef.Type)
	}

	if openaiDef.Function.Name != "test_tool" {
		t.Errorf("Expected name 'test_tool', got '%s'", openaiDef.Function.Name)
	}

	if openaiDef.Function.Description != "A test tool" {
		t.Errorf("Expected description 'A test tool', got '%s'", openaiDef.Function.Description)
	}
}

// TestToolToAnthropic tests the ToAnthropic conversion method
func TestToolToAnthropic(t *testing.T) {
	tool := NewTool(
		"test_tool",
		"A test tool",
		map[string]any{"type": "object"},
		nil,
	)

	anthropicDef := tool.ToAnthropic()

	if anthropicDef["name"] != "test_tool" {
		t.Errorf("Expected name 'test_tool', got '%v'", anthropicDef["name"])
	}

	if anthropicDef["description"] != "A test tool" {
		t.Errorf("Expected description 'A test tool', got '%v'", anthropicDef["description"])
	}

	if anthropicDef["input_schema"] == nil {
		t.Error("Expected input_schema to be present")
	}
}
