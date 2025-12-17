# Blaze Documentation

Welcome to the Blaze framework documentation. Blaze is a blazingly fast Go web framework with **built-in AI tools** for building Claude, OpenAI, and Gemini-compatible servers.

## Quick Links

| Topic | Description |
|-------|-------------|
| [Getting Started](../README.md) | Main README with installation and quick start |
| [Adapters](#adapters) | AI provider adapters for tool calling |
| [Built-in Tools](#built-in-tools) | Pre-built tools for AI agents |

---

## Adapters

Adapters enable AI models to call your Go functions as tools. Each adapter translates between the AI provider's API format and Blaze's internal tool system.

| Adapter | Documentation | Status |
|---------|--------------|--------|
| Anthropic (Claude) | [adapters/anthropic.md](adapters/anthropic.md) | ✅ Stable |
| OpenAI (GPT) | [adapters/openai.md](adapters/openai.md) | ✅ Stable |
| Gemini | Coming soon | 🚧 Planned |

### Architecture

```
┌─────────────────────────────────────────────────┐
│     AI Clients (Claude, GPT, Gemini, etc.)      │
└──────────────────────┬──────────────────────────┘
                       │ HTTP
         ┌─────────────┼─────────────┐
         ▼             ▼             ▼
   POST /chat    POST /openai   GET /tools
   (Anthropic)    (OpenAI)      (Discovery)
         │             │             │
         └─────────────┼─────────────┘
                       ▼
┌─────────────────────────────────────────────────┐
│              Shared Tool Registry               │
│   • Custom Tools (your Go functions)            │
│   • Built-in Tools (web, datetime, memory...)   │
└─────────────────────────────────────────────────┘
```

### Choosing an Adapter

- **AnthropicAdapter**: Use when integrating with Claude
- **OpenAIAdapter**: Use when integrating with GPT models or OpenAI-compatible APIs
- **ListToolsHandler**: Discovery endpoint that returns tools in all formats

---

## Built-in Tools

Blaze ships with a comprehensive toolkit ready for AI agents:

| Tool | Documentation | Description |
|------|--------------|-------------|
| Web Search | [tools/web.md](tools/web.md) | Search the internet (DuckDuckGo) |
| Web Read | [tools/web.md](tools/web.md) | Read webpages as Markdown |
| Web Fetch | [tools/web.md](tools/web.md) | Raw HTTP fetch for APIs |
| DateTime | [tools/datetime.md](tools/datetime.md) | Time operations and timezone handling |
| JSON Query | [tools/json-query.md](tools/json-query.md) | jq-like JSON querying |
| Memory | [tools/memory.md](tools/memory.md) | In-memory key-value store |

---

## Creating Custom Tools

Define your own tools with the `adapter.NewTool()` function:

```go
myTool := adapter.NewTool(
    "my_tool",                    // Name
    "Description for the AI",     // Description
    map[string]any{...},          // JSON Schema
    func(input json.RawMessage) (any, error) {
        // Your logic here
        return result, nil
    },
)
```

See the adapter documentation for detailed examples.

---

## Examples

Check out [examples/main.go](../examples/main.go) for a complete working example with:
- Custom tools (calculator, weather)
- All built-in tools
- Both Anthropic and OpenAI endpoints
- Tool discovery endpoint

---

## License

MIT - [LICENSE](../LICENSE)
