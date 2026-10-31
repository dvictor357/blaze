// Command mcp-server exposes Blaze tools to MCP clients such as Claude
// Desktop, Claude Code and Cursor.
//
// By default it speaks MCP over stdio, which is what desktop clients launch:
//
//	blaze-mcp
//
// With -http it serves Streamable HTTP on a loopback address instead, at /mcp,
// and requires "Authorization: Bearer <token>" (BLAZE_MCP_TOKEN, or a random
// token printed at startup):
//
//	BLAZE_MCP_TOKEN=change-me blaze-mcp -http 127.0.0.1:8090
//
// The tools are a small custom one (text_stats) plus Blaze's built-ins. Every
// call goes through the Blaze executor: arguments are validated against the
// schema, calls are time- and size-bounded, panics are contained, and web
// tools refuse private, loopback and link-local destinations.
//
// The memory tool's scope is attached by the server, never taken from tool
// arguments: over stdio there is one local user, and over HTTP each bearer
// token gets its own scope.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/dvictor357/blaze/adapter"
	blazemcp "github.com/dvictor357/blaze/mcp"
	"github.com/dvictor357/blaze/tool"
	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const version = "0.1.0"

// textStats is an ordinary Go function exposed as a tool. The executor
// validates arguments against the schema before it runs.
func textStats() adapter.Tool {
	return adapter.NewTool("text_stats", "Count the characters, words and lines of a text.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string", "minLength": 1, "maxLength": 10000},
			},
			"required":             []string{"text"},
			"additionalProperties": false,
		},
		func(ctx context.Context, input json.RawMessage) (any, error) {
			var in struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, err
			}
			return map[string]int{
				"characters": utf8.RuneCountInString(in.Text),
				"words":      len(strings.Fields(in.Text)),
				"lines":      strings.Count(in.Text, "\n") + 1,
			}, nil
		})
}

func newExecutor() (*adapter.Executor, error) {
	web := tool.NewWebClient(tool.WebClientConfig{})
	return adapter.NewExecutor(adapter.Config{},
		textStats(),
		tool.NewDateTimeTool(),
		tool.NewJSONQueryTool(),
		tool.NewMemoryTool(tool.NewMemoryStore(tool.MemoryOptions{})),
		tool.NewWebSearchTool(web),
		tool.NewWebReadTool(web),
		tool.NewWebFetchTool(web),
	)
}

func options(scope func(context.Context, *sdk.CallToolRequest) (string, error)) blazemcp.Options {
	return blazemcp.Options{
		Name:    "blaze",
		Version: version,
		Context: func(ctx context.Context, req *sdk.CallToolRequest) (context.Context, error) {
			s, err := scope(ctx, req)
			if err != nil {
				return nil, err
			}
			return tool.WithMemoryScope(ctx, s), nil
		},
	}
}

// stdioOptions: a stdio server is launched by one local user, so one scope.
func stdioOptions() blazemcp.Options {
	return options(func(context.Context, *sdk.CallToolRequest) (string, error) { return "local", nil })
}

// newHTTPHandler requires the bearer token and gives each verified token its
// own memory scope. A real deployment would verify per-user credentials
// (OAuth, API keys) here instead of one shared token.
func newHTTPHandler(ex *adapter.Executor, token string) http.Handler {
	want := sha256.Sum256([]byte(token))
	verify := func(ctx context.Context, got string, r *http.Request) (*auth.TokenInfo, error) {
		have := sha256.Sum256([]byte(got))
		if subtle.ConstantTimeCompare(have[:], want[:]) != 1 {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: "token:" + hex.EncodeToString(have[:8]), Expiration: time.Now().Add(time.Hour)}, nil
	}
	h := blazemcp.NewHTTPHandler(ex, options(func(ctx context.Context, req *sdk.CallToolRequest) (string, error) {
		if req.Extra == nil || req.Extra.TokenInfo == nil || req.Extra.TokenInfo.UserID == "" {
			return "", errors.New("unauthenticated")
		}
		return req.Extra.TokenInfo.UserID, nil
	}))
	mux := http.NewServeMux()
	mux.Handle("/mcp", auth.RequireBearerToken(verify, nil)(h))
	return mux
}

func tokenFromEnv(stderr io.Writer) (string, error) {
	if t := os.Getenv("BLAZE_MCP_TOKEN"); t != "" {
		return t, nil
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	t := hex.EncodeToString(buf)
	fmt.Fprintf(stderr, "BLAZE_MCP_TOKEN not set; using a random token for this run: %s\n", t)
	return t, nil
}

func serveHTTP(ctx context.Context, ex *adapter.Executor, addr string, stderr io.Writer) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid -http address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		fmt.Fprintf(stderr, "WARNING: %s is not a loopback address; put TLS in front and keep the token secret\n", addr)
	}
	token, err := tokenFromEnv(stderr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           newHTTPHandler(ex, token),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Long enough for a full batch (the executor's MaxDuration) plus slack.
		WriteTimeout:   ex.Config().MaxDuration + 10*time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 64 << 10,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	fmt.Fprintf(stderr, "Blaze MCP server listening on http://%s/mcp\n", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func run(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("blaze-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	httpAddr := fs.String("http", "", "serve Streamable HTTP on this address (for example 127.0.0.1:8090) instead of stdio")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ex, err := newExecutor()
	if err != nil {
		return err
	}
	if *httpAddr != "" {
		return serveHTTP(ctx, ex, *httpAddr, stderr)
	}
	return blazemcp.ServeStdio(ctx, ex, stdioOptions())
}

func main() {
	// Standard output carries the MCP protocol in stdio mode: log to stderr.
	log.SetOutput(os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		log.Fatal(err)
	}
}
