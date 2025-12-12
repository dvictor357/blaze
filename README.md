# Blaze 🔥

A blazingly fast Go web framework with **built-in AI tools** for building Claude, Gemini, and Codex-compatible servers.

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

## Why Blaze?

Most web frameworks just handle HTTP. **Blaze goes further** — it ships with a complete AI toolkit that transforms your server into an intelligent agent endpoint.

| Feature | Blaze | Gin | Echo | Fiber |
|---------|:-----:|:---:|:----:|:-----:|
| Fast HTTP routing | ✅ | ✅ | ✅ | ✅ |
| **Radix tree router** | ✅ | ✅ | ✅ | ❌ |
| Middleware support | ✅ | ✅ | ✅ | ✅ |
| JSON streaming | ✅ | ❌ | ❌ | ❌ |
| **AI Tool Adapter** | ✅ | ❌ | ❌ | ❌ |
| **Built-in Web Search** | ✅ | ❌ | ❌ | ❌ |
| **HTML→Markdown** | ✅ | ❌ | ❌ | ❌ |
| **Memory/State** | ✅ | ❌ | ❌ | ❌ |
| Zero dependencies | ✅ | ❌ | ❌ | ❌ |

### Performance

```
BenchmarkRouter_Static-14     8,653,634    126.6 ns/op    96 B/op    2 allocs/op
BenchmarkRouter_Param-14     10,620,004    109.2 ns/op   400 B/op    3 allocs/op
BenchmarkRouter_Mixed-14      3,767,991    311.8 ns/op   848 B/op    8 allocs/op
```

> Tested on Apple M4 Pro with 100 routes

## Quick Start

```bash
go get github.com/dvictor357/blaze
```

```go
package main

import "github.com/dvictor357/blaze"

func main() {
    e := blaze.New()
    
    e.GET("/", func(c *blaze.Context) error {
        return c.JSON(200, map[string]string{"message": "Hello, Blaze!"})
    })
    
    e.Listen(":8080")
}
```

## Core Features

### Routing

```go
e := blaze.New()

e.GET("/users", listUsers)
e.POST("/users", createUser)
e.GET("/users/:id", getUser)           // Path parameters
e.PUT("/users/:id", updateUser)
e.DELETE("/users/:id", deleteUser)
e.GET("/files/*filepath", serveFile)   // Wildcard

// Route groups
api := e.Group("/api")
api.Use(authMiddleware)  // Group-specific middleware
api.GET("/users", listUsersAPI)

v1 := api.Group("/v1")   // Nested: /api/v1/...
v1.GET("/status", getStatus)
```

### Middleware

```go
e := blaze.New()

// Built-in middleware
e.Use(blaze.Logger())    // Request logging
e.Use(blaze.Recovery())  // Panic recovery

// Custom middleware
e.Use(func(next blaze.HandlerFunc) blaze.HandlerFunc {
    return func(c *blaze.Context) error {
        c.ResponseWriter.Header().Set("X-Custom", "value")
        return next(c)
    }
})
```

### Context

```go
func handler(c *blaze.Context) error {
    // Path parameters
    id := c.Param("id")
    
    // JSON response
    return c.JSON(200, data)
    
    // String response
    return c.String(200, "Hello")
    
    // Bind JSON body
    var req MyRequest
    c.BindJSON(&req)
    
    // Streaming JSON (for AI tools)
    return c.StreamJSON(dataChan)
}
```

---

## 🤖 AI Tools Adapter

Blaze's killer feature: **turn any Go function into a Claude-callable tool**.

### How It Works

```
┌─────────────────────────────────────┐
│         Claude / Gemini / AI        │
└──────────────┬──────────────────────┘
               │ HTTP POST /chat
               ▼
┌─────────────────────────────────────┐
│         Blaze Framework             │
│         AnthropicAdapter            │
└──────────────┬──────────────────────┘
               │
        ┌──────┴──────┐
        ▼             ▼
   Your Tool      Built-in Tools
   (Go func)      (web, datetime...)
```

### Basic Example

```go
package main

import (
    "encoding/json"
    "github.com/dvictor357/blaze"
    "github.com/dvictor357/blaze/adapter"
    "github.com/dvictor357/blaze/tool"
)

func main() {
    e := blaze.New()
    
    // Create a custom tool
    calculatorTool := adapter.NewTool(
        "calculator",
        "Perform mathematical calculations",
        map[string]any{
            "type": "object",
            "properties": map[string]any{
                "expression": map[string]any{
                    "type": "string",
                    "description": "Math expression to evaluate",
                },
            },
            "required": []string{"expression"},
        },
        func(input json.RawMessage) (any, error) {
            var data struct {
                Expression string `json:"expression"`
            }
            json.Unmarshal(input, &data)
            // ... evaluate expression
            return map[string]any{"result": 42}, nil
        },
    )
    
    // Register adapter with built-in tools
    e.POST("/chat", adapter.AnthropicAdapter(
        calculatorTool,
        tool.NewWebSearchTool(),
        tool.NewDateTimeTool(),
    ))
    
    e.Listen(":8080")
}
```

---

## 🧰 Built-in Tools

Blaze ships with a comprehensive toolkit ready for AI agents:

### Web Tools

#### `web_search` — Search the Internet
Zero API keys required. Uses DuckDuckGo.

```json
{
  "name": "web_search",
  "input": {
    "query": "golang best practices 2024",
    "max_results": 5
  }
}
```

#### `web_read` — Read Webpages as Markdown
Converts HTML to clean, token-efficient Markdown. Extracts main content, strips navigation/ads.

```json
{
  "name": "web_read",
  "input": {
    "url": "https://go.dev/doc/effective_go"
  }
}
```

**Output:**
```json
{
  "title": "Effective Go",
  "description": "Tips for writing clear, idiomatic Go code",
  "content": "# Effective Go\n\nGo is a new language...",
  "links": [{"url": "...", "text": "..."}],
  "truncated": false
}
```

#### `web_fetch` — Raw HTTP Fetch
For APIs, JSON endpoints, or when you need raw response.

```json
{
  "name": "web_fetch",
  "input": {
    "url": "https://api.github.com/users/golang",
    "headers": {"Accept": "application/json"}
  }
}
```

---

### Essential Tools

#### `datetime` — Time & Timezone Operations

```json
// Get current time
{"action": "now", "timezone": "America/New_York"}

// Parse a date
{"action": "parse", "date": "2024-12-25"}

// Calculate difference
{"action": "diff", "date": "2024-01-01", "date2": "2024-12-31"}

// Add duration
{"action": "add", "date": "2024-01-01", "duration": "30d"}
```

**Capabilities:**
- Current time in any timezone
- Parse various date formats
- Calculate time differences
- Add/subtract durations (hours, days, etc.)
- Format dates (ISO, RFC822, Unix, human-readable)

---

#### `json_query` — jq-like JSON Querying

```json
{
  "json": "{\"users\": [{\"name\": \"Alice\", \"age\": 30}, {\"name\": \"Bob\", \"age\": 25}]}",
  "query": ".users[?age>25].name"
}
```

**Query Syntax:**
| Syntax | Description | Example |
|--------|-------------|---------|
| `.field` | Access field | `.data.name` |
| `[n]` | Array index | `.users[0]` |
| `[n:m]` | Array slice | `.items[0:5]` |
| `[*]` | Wildcard | `.users[*].email` |
| `[?cond]` | Filter | `[?status=="active"]` |

**Actions:**
- `get` — Extract value (default)
- `keys` — List object keys
- `length` — Count items
- `type` — Get JSON type
- `flatten` — Flatten nested arrays
- `unique` — Deduplicate array

---

#### `memory` — In-Memory Key-Value Store

Persist data across tool calls. Thread-safe with TTL support.

```json
// Store a value
{"action": "set", "key": "user_preference", "value": "dark_mode", "ttl": 3600}

// Retrieve
{"action": "get", "key": "user_preference"}

// Counter operations
{"action": "incr", "key": "request_count"}
{"action": "decr", "key": "request_count"}

// List operations
{"action": "append", "key": "history", "value": "searched: golang"}
{"action": "lrange", "key": "history", "start": 0, "end": -1}
```

**Features:**
- Key-value with optional TTL
- Counters (incr/decr)
- Lists (append, pop, range)
- Thread-safe for concurrent access

---

## 📁 Project Structure

```
blaze/
├── blaze.go         # Engine & HTTP methods
├── router.go        # URL routing with params
├── context.go       # Request/Response context
├── middleware.go    # Logger, Recovery
├── adapter/
│   └── anthropic_adapter.go  # Claude-compatible adapter
├── tool/
│   ├── web_search.go    # DuckDuckGo search
│   ├── web_read.go      # HTML→Markdown reader
│   ├── web_fetcher.go   # Raw HTTP fetch
│   ├── datetime.go      # Time operations
│   ├── json_query.go    # JSON querying
│   └── memory.go        # Key-value store
└── examples/
    └── main.go          # Full example
```

---

## Complete Example

```go
package main

import (
    "encoding/json"
    "fmt"

    "github.com/dvictor357/blaze"
    "github.com/dvictor357/blaze/adapter"
    "github.com/dvictor357/blaze/tool"
)

func main() {
    e := blaze.New()
    e.Use(blaze.Logger())
    e.Use(blaze.Recovery())

    // Health check
    e.GET("/", func(c *blaze.Context) error {
        return c.JSON(200, map[string]string{
            "status": "ok",
            "tools":  "8 available",
        })
    })

    // AI endpoint with all tools
    e.POST("/chat", adapter.AnthropicAdapter(
        // Web Tools
        tool.NewWebSearchTool(),
        tool.NewWebReadTool(),
        tool.NewWebFetchTool(),
        // Essential Tools
        tool.NewDateTimeTool(),
        tool.NewJSONQueryTool(),
        tool.NewMemoryTool(),
    ))

    fmt.Println("🔥 Blaze running on :8080")
    e.Listen(":8080")
}
```

---

## Testing

```bash
# Build
go build ./...

# Run example
go run examples/main.go

# Test health endpoint
curl http://localhost:8080/

# Test AI endpoint (Claude format)
curl -X POST http://localhost:8080/chat \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet",
    "messages": [{"role": "user", "content": "What time is it?"}]
  }'
```

---

## Extending

### Create Custom Tools

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

### Add New Adapters

Create adapters for other AI providers:

```go
// adapter/openai_adapter.go
func OpenAIAdapter(tools ...Tool) blaze.HandlerFunc {
    // Implement OpenAI's tool calling format
}
```

---

## Roadmap

- [ ] OpenAI adapter
- [ ] Gemini adapter
- [ ] File system tools (sandboxed)
- [ ] Shell execution (sandboxed)
- [ ] WebSocket support
- [ ] Rate limiting middleware
- [ ] Authentication middleware

---

## License

MIT License - see [LICENSE](LICENSE) for details.

---

**Built with 🔥 by the Blaze team**
