# Blaze Adapters

This package provides AI provider adapters for the Blaze framework.

## Available Adapters

| Adapter | Function | Documentation |
|---------|----------|---------------|
| Anthropic | `AnthropicAdapter()` | [docs/adapters/anthropic.md](../docs/adapters/anthropic.md) |
| OpenAI | `OpenAIAdapter()` | [docs/adapters/openai.md](../docs/adapters/openai.md) |

## Quick Example

```go
import "github.com/dvictor357/blaze/adapter"

// Create tools
tools := []adapter.Tool{
    adapter.NewTool("my_tool", "Description", schema, handler),
}

// Register adapters
engine.POST("/chat", adapter.AnthropicAdapter(tools...))
engine.POST("/openai", adapter.OpenAIAdapter(tools...))
engine.GET("/tools", adapter.ListToolsHandler(tools...))
```

See [docs/](../docs/) for full documentation.
