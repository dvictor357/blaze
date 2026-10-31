package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain lets the test binary act as the stdio server, so the test below
// drives the real main over real pipes, as Claude Desktop or Cursor would.
func TestMain(m *testing.M) {
	if os.Getenv("BLAZE_MCP_EXAMPLE_CHILD") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func textOf(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("empty result")
	}
	return res.Content[0].(*sdk.TextContent).Text
}

func exercise(t *testing.T, cs *sdk.ClientSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 7 {
		t.Fatalf("listed %d tools, want 7", len(list.Tools))
	}

	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "text_stats", Arguments: map[string]any{"text": "hello world\nBlaze"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || textOf(t, res) != `{"characters":17,"lines":2,"words":3}` {
		t.Fatalf("text_stats: %s", textOf(t, res))
	}

	res, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: "text_stats", Arguments: map[string]any{"text": ""}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(textOf(t, res), "invalid_arguments") {
		t.Fatalf("empty text must fail schema validation: %s", textOf(t, res))
	}

	for _, args := range []map[string]any{{"action": "set", "key": "k", "value": 7}, {"action": "get", "key": "k"}} {
		res, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: "memory", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("memory %v: %s", args, textOf(t, res))
		}
	}
	if !strings.Contains(textOf(t, res), "7") {
		t.Fatalf("memory get: %s", textOf(t, res))
	}

	res, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: "web_fetch", Arguments: map[string]any{"url": "http://127.0.0.1:1/"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("web_fetch to loopback must be refused: %s", textOf(t, res))
	}
}

func TestStdio(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "BLAZE_MCP_EXAMPLE_CHILD=1")
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	exercise(t, cs)
}

type bearer string

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

func TestHTTP(t *testing.T) {
	ex, err := newExecutor()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newHTTPHandler(ex, "test-token"))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request: status %d", resp.StatusCode)
	}

	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer("test-token"), Timeout: 20 * time.Second},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	exercise(t, cs)
}
