# Blaze MCP server example

Serves Blaze tools to MCP clients (Claude Desktop, Claude Code, Cursor, or anything else that speaks MCP) over stdio or Streamable HTTP.

Tools: `text_stats` (a custom Go function), `datetime`, `json_query`, `memory`, `web_search`, `web_read`, `web_fetch`.

Each call goes through the Blaze executor, so arguments are validated against the tool's schema, calls are time- and size-bounded, panics are contained, and web tools refuse private, loopback and link-local destinations. The memory scope is attached by the server, never taken from tool arguments.

## Build

This example is its own Go module. From the repository root:

```sh
cd examples/mcp-server
go build -o blaze-mcp .
```

Use the absolute path of the resulting `blaze-mcp` binary in the client configs below (shown as `/path/to/blaze-mcp`).

## stdio (local clients)

Desktop clients start the binary and talk to it over stdin/stdout. Nothing else is written to stdout; logs go to stderr.

### Claude Code

```sh
claude mcp add blaze -- /path/to/blaze-mcp
```

### Claude Desktop

Edit `claude_desktop_config.json` (Settings → Developer → Edit Config) and restart Claude Desktop:

```json
{
  "mcpServers": {
    "blaze": {
      "command": "/path/to/blaze-mcp"
    }
  }
}
```

### Cursor

Add to `~/.cursor/mcp.json` (all projects) or `.cursor/mcp.json` (one project):

```json
{
  "mcpServers": {
    "blaze": {
      "command": "/path/to/blaze-mcp"
    }
  }
}
```

## Streamable HTTP

```sh
export BLAZE_MCP_TOKEN="$(openssl rand -hex 16)"
./blaze-mcp -http 127.0.0.1:8090
```

The endpoint is `http://127.0.0.1:8090/mcp` and every request needs `Authorization: Bearer $BLAZE_MCP_TOKEN`. Without `BLAZE_MCP_TOKEN`, a random token is printed to stderr at startup. The handler is stateless and answers each request with one JSON response; it rejects DNS-rebinding `Host` headers, cross-origin browser requests and bodies over 1 MiB. Serve it behind TLS before exposing it beyond loopback.

### Claude Code

```sh
claude mcp add --transport http blaze http://127.0.0.1:8090/mcp \
  --header "Authorization: Bearer $BLAZE_MCP_TOKEN"
```

### Cursor

```json
{
  "mcpServers": {
    "blaze": {
      "url": "http://127.0.0.1:8090/mcp",
      "headers": { "Authorization": "Bearer ${env:BLAZE_MCP_TOKEN}" }
    }
  }
}
```

### Quick check with curl

```sh
curl -s http://127.0.0.1:8090/mcp \
  -H "Authorization: Bearer $BLAZE_MCP_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"text_stats","arguments":{"text":"hello world"}}}'
```

## Using your own tools

The MCP part is one call. Given any `*adapter.Executor`:

```go
import blazemcp "github.com/dvictor357/blaze/mcp"

// stdio
err := blazemcp.ServeStdio(ctx, ex, blazemcp.Options{Name: "my-tools", Version: "1.0.0"})

// Streamable HTTP: mount behind your authentication
mux.Handle("/mcp", requireAuth(blazemcp.NewHTTPHandler(ex, blazemcp.Options{Context: attachScope})))
```

See [`main.go`](main.go) for bearer-token authentication and per-caller memory scopes, and [docs/mcp.md](../../docs/mcp.md) for details.
