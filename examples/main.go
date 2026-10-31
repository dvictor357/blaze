// Command main is a local tool-execution server built on Blaze.
//
// Blaze does not call any model. The application that owns the model sends the
// model its tool definitions (GET /v1/tools), then forwards the model's tool
// calls here as explicit invocations (POST /v1/invoke) and feeds the results
// back to the model.
//
// Security model of this example:
//   - It listens on 127.0.0.1 by default (override with BLAZE_ADDR).
//   - Every /v1 route requires "Authorization: Bearer <token>". The token is
//     BLAZE_TOKEN, or a random one printed at startup. Authentication is the
//     application's responsibility, not Blaze's.
//   - The memory tool's scope is derived server-side from the authenticated
//     identity and attached to the request context. It never comes from the
//     request body. This example has a single identity, so there is a single
//     scope; a multi-user server would map each verified credential to its
//     own scope.
//   - Web tools refuse private, loopback and link-local destinations.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/dvictor357/blaze"
	"github.com/dvictor357/blaze/adapter"
	"github.com/dvictor357/blaze/tool"
)

const maxTextStatsBytes = 10_000

// textStatsTool is a small deterministic custom tool. It validates its own
// input: Blaze passes a JSON object but does not enforce the schema.
func textStatsTool() adapter.Tool {
	return adapter.NewTool(
		"text_stats",
		"Count the characters, words and lines of a text",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "The text to analyze (at most 10000 bytes)",
				},
			},
			"required": []string{"text"},
		},
		func(ctx context.Context, input json.RawMessage) (any, error) {
			var data struct {
				Text string `json:"text"`
			}
			dec := json.NewDecoder(bytes.NewReader(input))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&data); err != nil {
				return nil, fmt.Errorf("invalid input: %w", err)
			}
			if data.Text == "" {
				return nil, errors.New("text is required")
			}
			if len(data.Text) > maxTextStatsBytes {
				return nil, fmt.Errorf("text exceeds %d bytes", maxTextStatsBytes)
			}
			return map[string]int{
				"characters": utf8.RuneCountInString(data.Text),
				"words":      len(strings.Fields(data.Text)),
				"lines":      strings.Count(data.Text, "\n") + 1,
			}, nil
		},
	)
}

// authenticate requires the bearer token and attaches the trusted memory
// scope for that identity to the request context.
func authenticate(token string) blaze.MiddlewareFunc {
	want := sha256.Sum256([]byte(token))
	scope := "token:" + hex.EncodeToString(want[:])[:16]

	return func(next blaze.HandlerFunc) blaze.HandlerFunc {
		return func(c *blaze.Context) error {
			got, ok := strings.CutPrefix(c.Request.Header.Get("Authorization"), "Bearer ")
			have := sha256.Sum256([]byte(got))
			if !ok || subtle.ConstantTimeCompare(have[:], want[:]) != 1 {
				c.SetHeader("WWW-Authenticate", "Bearer")
				return &blaze.HTTPError{Status: http.StatusUnauthorized, Message: "unauthorized"}
			}
			ctx := tool.WithMemoryScope(c.Request.Context(), scope)
			c.Request = c.Request.WithContext(ctx)
			return next(c)
		}
	}
}

// newEngine builds the server. The memory store is owned here and private to
// this engine.
func newEngine(token string) (*blaze.Engine, error) {
	web := tool.NewWebClient(tool.WebClientConfig{})
	memory := tool.NewMemoryStore(tool.MemoryOptions{})

	ex, err := adapter.NewExecutor(adapter.Config{},
		textStatsTool(),
		tool.NewWebSearchTool(web),
		tool.NewWebReadTool(web),
		tool.NewWebFetchTool(web),
		tool.NewDateTimeTool(),
		tool.NewJSONQueryTool(),
		tool.NewMemoryTool(memory),
	)
	if err != nil {
		return nil, err
	}

	e := blaze.New()
	e.Use(blaze.Recovery(), blaze.Logger())

	e.GET("/{$}", func(c *blaze.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	v1 := e.Group("/v1")
	v1.Use(authenticate(token))
	v1.GET("/tools", adapter.ListToolsHandler(ex))
	v1.POST("/invoke", adapter.InvokeHandler(ex))
	return e, nil
}

func tokenFromEnv() (string, error) {
	if t := os.Getenv("BLAZE_TOKEN"); t != "" {
		return t, nil
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	t := hex.EncodeToString(buf)
	fmt.Printf("BLAZE_TOKEN not set; using a random token for this run: %s\n", t)
	return t, nil
}

func main() {
	addr := os.Getenv("BLAZE_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	if host, _, err := net.SplitHostPort(addr); err != nil {
		log.Fatalf("invalid BLAZE_ADDR %q: %v", addr, err)
	} else if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		log.Printf("WARNING: %s is not a loopback address; put TLS in front and keep the token secret", addr)
	}

	token, err := tokenFromEnv()
	if err != nil {
		log.Fatalf("generate token: %v", err)
	}
	e, err := newEngine(token)
	if err != nil {
		log.Fatalf("build server: %v", err)
	}

	fmt.Println("Blaze tool server")
	fmt.Println("  GET  /v1/tools   - tool definitions (OpenAI and Anthropic formats)")
	fmt.Println("  POST /v1/invoke  - execute explicit tool calls (JSON or SSE)")
	if err := e.Listen(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server stopped: %v", err)
	}
}
