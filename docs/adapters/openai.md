# OpenAI Adapter

An adapter that enables OpenAI GPT models and OpenAI-compatible clients to interact with Blaze tools using the Chat Completions API format.

## Overview

The OpenAI Adapter processes OpenAI-format tool calling requests, enabling GPT models and any system using the OpenAI Chat Completions API to use the same tools as Claude.

## Quick Start

### 1. Define Your Tools (Same as Anthropic)

```go
tools := []adapter.Tool{
    adapter.NewTool(
        "calculator",
        "Perform calculations",
        map[string]any{
            "type": "object",
            "properties": map[string]any{
                "expression": map[string]any{"type": "string"},
            },
            "required": []string{"expression"},
        },
        func(input json.RawMessage) (any, error) {
            // Your logic here
            return map[string]any{"result": 42}, nil
        },
    ),
    tool.NewWebSearchTool(),
    tool.NewDateTimeTool(),
}
```

### 2. Register the Adapter

```go
engine := blaze.New()

// OpenAI-compatible endpoint
engine.POST("/openai", adapter.OpenAIAdapter(tools...))

// Optional: Tool discovery
engine.GET("/tools", adapter.ListToolsHandler(tools...))

engine.Listen(":8080")
```

### 3. Call with OpenAI Format

```bash
curl -X POST http://localhost:8080/openai \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [
      {"role": "user", "content": "What time is it in Tokyo?"},
      {
        "role": "assistant",
        "tool_calls": [{
          "id": "call_abc123",
          "type": "function",
          "function": {
            "name": "datetime",
            "arguments": "{\"action\": \"now\", \"timezone\": \"Asia/Tokyo\"}"
          }
        }]
      }
    ]
  }'
```

---

## How It Works

```
OpenAI-Compatible Client
    ↓ HTTP POST /openai (JSON with tool_calls)
Blaze Router
    ↓ Routes to OpenAIAdapter
OpenAIAdapter
    ↓ Parses request
    ↓ Finds tool_calls in assistant message
    ↓ Executes tool handlers
    ↓ Formats response (streaming or regular)
Client
    ↓ Receives tool results
```

---

## Request Format

OpenAI uses `tool_calls` in assistant messages:

```json
{
  "model": "gpt-4",
  "messages": [
    {"role": "user", "content": "Search for golang"},
    {
      "role": "assistant",
      "tool_calls": [{
        "id": "call_1",
        "type": "function",
        "function": {
          "name": "web_search",
          "arguments": "{\"query\": \"golang best practices\"}"
        }
      }]
    }
  ]
}
```

## Response Format

The adapter returns OpenAI Chat Completions format:

```json
{
  "id": "chatcmpl-123456789",
  "object": "chat.completion",
  "created": 1734444307,
  "model": "gpt-4",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "{\"results\": [...]}"
    },
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 50,
    "total_tokens": 60
  }
}
```

---

## Streaming Support

Enable streaming with `"stream": true`:

```json
{
  "model": "gpt-4",
  "stream": true,
  "messages": [...]
}
```

Response uses Server-Sent Events with chunked deltas.

---

## Tool Discovery

The `ListToolsHandler` returns tools in multiple formats:

```go
engine.GET("/tools", adapter.ListToolsHandler(tools...))
```

**Response:**

```json
{
  "openai": [
    {
      "type": "function",
      "function": {
        "name": "web_search",
        "description": "Search the web",
        "parameters": {...}
      }
    }
  ],
  "anthropic": [
    {
      "name": "web_search",
      "description": "Search the web",
      "input_schema": {...}
    }
  ],
  "count": 8
}
```

---

## Format Conversion

Convert tools between formats programmatically:

```go
// To OpenAI format
openaiDef := myTool.ToOpenAI()
// Returns: OpenAIToolDef{Type: "function", Function: {...}}

// To Anthropic format
anthropicDef := myTool.ToAnthropic()
// Returns: map[string]any{"name": "...", "input_schema": {...}}
```

---

## Error Handling

### Tool Not Found

```json
{
  "choices": [{
    "message": {
      "content": "{\"error\": \"Tool 'unknown' not found\"}"
    }
  }]
}
```

### Invalid Request

```json
{
  "error": {
    "message": "Invalid request: ...",
    "type": "invalid_request_error"
  }
}
```

---

## Comparison: OpenAI vs Anthropic

| Aspect | Anthropic | OpenAI |
|--------|-----------|--------|
| **Endpoint** | `POST /chat` | `POST /openai` |
| **Tool Def** | `input_schema` | `function.parameters` |
| **Tool Calls** | `content[].tool_use` | `tool_calls[]` |
| **Tool Results** | `tool_result` block | `role: "tool"` message |

Both adapters share the same `Tool` struct and handlers.

---

## Features

| Feature | Status |
|---------|--------|
| Chat Completions Format | ✅ |
| Tool Execution | ✅ |
| Streaming Responses | ✅ |
| Multi-format Discovery | ✅ |
| Error Handling | ✅ |
| Type Safe Handlers | ✅ |

---

## See Also

- [Anthropic Adapter](anthropic.md) - For Claude
- [Built-in Tools](../tools/) - Pre-built tools
- [Examples](../../examples/main.go) - Complete working example
