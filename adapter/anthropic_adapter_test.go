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

// TestAnthropicAdapter_ToolExecution tests that tool_use blocks are executed correctly
func TestAnthropicAdapter_ToolExecution(t *testing.T) {
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

	e := blaze.New()
	e.POST("/chat", AnthropicAdapter(echoTool))

	// Create request with tool_use block
	reqBody := AnthropicChatRequest{
		Model: "claude-3-5-sonnet",
		Messages: []AnthropicMessage{
			{
				Role: "user",
				Content: []AnthropicContentBlock{
					{
						Type:  "tool_use",
						ID:    "toolu_123",
						Name:  "echo",
						Input: map[string]any{"message": "hello world"},
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var resp AnthropicChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if len(resp.Content) != 1 {
		t.Fatalf("Expected 1 content block, got %d", len(resp.Content))
	}

	if resp.Content[0].Type != "tool_result" {
		t.Errorf("Expected type 'tool_result', got '%s'", resp.Content[0].Type)
	}

	if !strings.Contains(resp.Content[0].Content, "hello world") {
		t.Errorf("Expected content to contain 'hello world', got: %s", resp.Content[0].Content)
	}
}

// TestAnthropicAdapter_NoToolUse tests response when no tool_use blocks are present
func TestAnthropicAdapter_NoToolUse(t *testing.T) {
	echoTool := NewTool("echo", "Echo back the input", nil, nil)

	e := blaze.New()
	e.POST("/chat", AnthropicAdapter(echoTool))

	reqBody := AnthropicChatRequest{
		Model: "claude-3-5-sonnet",
		Messages: []AnthropicMessage{
			{Role: "user", Content: "Hello, Claude!"},
		},
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var resp AnthropicChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if len(resp.Content) != 1 {
		t.Fatalf("Expected 1 content block, got %d", len(resp.Content))
	}

	if !strings.Contains(resp.Content[0].Text, "1 tools") {
		t.Errorf("Expected content to mention available tools, got: %s", resp.Content[0].Text)
	}
}

// TestAnthropicAdapter_ToolNotFound tests error handling for unknown tools
func TestAnthropicAdapter_ToolNotFound(t *testing.T) {
	echoTool := NewTool("echo", "Echo back the input", nil, nil)

	e := blaze.New()
	e.POST("/chat", AnthropicAdapter(echoTool))

	reqBody := AnthropicChatRequest{
		Model: "claude-3-5-sonnet",
		Messages: []AnthropicMessage{
			{
				Role: "user",
				Content: []AnthropicContentBlock{
					{
						Type:  "tool_use",
						ID:    "toolu_456",
						Name:  "unknown_tool",
						Input: map[string]any{},
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var resp AnthropicChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if !strings.Contains(resp.Content[0].Content, "not found") {
		t.Errorf("Expected error message about tool not found, got: %s", resp.Content[0].Content)
	}
}

// TestAnthropicAdapter_InvalidRequest tests error handling for invalid requests
func TestAnthropicAdapter_InvalidRequest(t *testing.T) {
	e := blaze.New()
	e.POST("/chat", AnthropicAdapter())

	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader("invalid json"))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rec.Code)
	}
}

// TestAnthropicAdapter_EmptyMessages tests error handling for empty messages
func TestAnthropicAdapter_EmptyMessages(t *testing.T) {
	e := blaze.New()
	e.POST("/chat", AnthropicAdapter())

	reqBody := AnthropicChatRequest{
		Model:    "claude-3-5-sonnet",
		Messages: []AnthropicMessage{},
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rec.Code)
	}
}

// TestAnthropicAdapter_LastMessageNotUser tests error when last message isn't from user
func TestAnthropicAdapter_LastMessageNotUser(t *testing.T) {
	e := blaze.New()
	e.POST("/chat", AnthropicAdapter())

	reqBody := AnthropicChatRequest{
		Model: "claude-3-5-sonnet",
		Messages: []AnthropicMessage{
			{Role: "assistant", Content: "Hello!"},
		},
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rec.Code)
	}
}

// TestAnthropicAdapter_MultipleToolCalls tests executing multiple tools in one request
func TestAnthropicAdapter_MultipleToolCalls(t *testing.T) {
	addTool := NewTool("add", "Add numbers", nil,
		func(input json.RawMessage) (any, error) {
			return map[string]any{"result": 42}, nil
		},
	)
	multiplyTool := NewTool("multiply", "Multiply numbers", nil,
		func(input json.RawMessage) (any, error) {
			return map[string]any{"result": 100}, nil
		},
	)

	e := blaze.New()
	e.POST("/chat", AnthropicAdapter(addTool, multiplyTool))

	reqBody := AnthropicChatRequest{
		Model: "claude-3-5-sonnet",
		Messages: []AnthropicMessage{
			{
				Role: "user",
				Content: []AnthropicContentBlock{
					{Type: "tool_use", ID: "toolu_1", Name: "add", Input: map[string]any{}},
					{Type: "tool_use", ID: "toolu_2", Name: "multiply", Input: map[string]any{}},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var resp AnthropicChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if len(resp.Content) != 2 {
		t.Errorf("Expected 2 tool results, got %d", len(resp.Content))
	}
}

// TestAnthropicAdapter_ResponseFormat tests the response format matches Anthropic spec
func TestAnthropicAdapter_ResponseFormat(t *testing.T) {
	tool := NewTool("test", "Test tool", nil,
		func(input json.RawMessage) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	)

	e := blaze.New()
	e.POST("/chat", AnthropicAdapter(tool))

	reqBody := AnthropicChatRequest{
		Model: "claude-3-5-sonnet",
		Messages: []AnthropicMessage{
			{
				Role: "user",
				Content: []AnthropicContentBlock{
					{Type: "tool_use", ID: "toolu_test", Name: "test", Input: map[string]any{}},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var resp AnthropicChatResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)

	// Verify required fields
	if resp.ID == "" {
		t.Error("Expected non-empty ID")
	}
	if resp.Type != "message" {
		t.Errorf("Expected type 'message', got '%s'", resp.Type)
	}
	if resp.Role != "assistant" {
		t.Errorf("Expected role 'assistant', got '%s'", resp.Role)
	}
	if resp.Model != "claude-3-5-sonnet" {
		t.Errorf("Expected model 'claude-3-5-sonnet', got '%s'", resp.Model)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("Expected stop_reason 'end_turn', got '%s'", resp.StopReason)
	}
}

// TestParseContentBlocks tests the content parsing function
func TestParseContentBlocks(t *testing.T) {
	// Test string content
	blocks := parseContentBlocks("Hello world")
	if len(blocks) != 1 || blocks[0].Type != "text" || blocks[0].Text != "Hello world" {
		t.Error("Failed to parse string content")
	}

	// Test array content
	arrayContent := []AnthropicContentBlock{
		{Type: "text", Text: "Hello"},
		{Type: "tool_use", ID: "123", Name: "test"},
	}
	blocks = parseContentBlocks(arrayContent)
	if len(blocks) != 2 {
		t.Errorf("Expected 2 blocks, got %d", len(blocks))
	}
}
