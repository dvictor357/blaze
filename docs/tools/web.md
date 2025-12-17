# Built-in Tools

Blaze ships with a comprehensive toolkit ready for AI agents. All tools work with both Anthropic and OpenAI adapters.

## Web Tools

### `web_search` — Search the Internet

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

**Response:**
```json
{
  "results": [
    {
      "title": "Effective Go",
      "url": "https://go.dev/doc/effective_go",
      "snippet": "Tips for writing clear, idiomatic Go code..."
    }
  ]
}
```

---

### `web_read` — Read Webpages as Markdown

Converts HTML to clean, token-efficient Markdown. Extracts main content, strips navigation/ads.

```json
{
  "name": "web_read",
  "input": {
    "url": "https://go.dev/doc/effective_go"
  }
}
```

**Response:**
```json
{
  "title": "Effective Go",
  "description": "Tips for writing clear, idiomatic Go code",
  "content": "# Effective Go\n\nGo is a new language...",
  "links": [{"url": "...", "text": "..."}],
  "truncated": false
}
```

---

### `web_fetch` — Raw HTTP Fetch

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

## Usage

```go
import "github.com/dvictor357/blaze/tool"

tools := []adapter.Tool{
    tool.NewWebSearchTool(),
    tool.NewWebReadTool(),
    tool.NewWebFetchTool(),
}
```

---

## See Also

- [DateTime Tool](datetime.md)
- [JSON Query Tool](json-query.md)
- [Memory Tool](memory.md)
