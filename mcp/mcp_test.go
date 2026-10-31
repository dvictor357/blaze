package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dvictor357/blaze/adapter"
	blazemcp "github.com/dvictor357/blaze/mcp"
	"github.com/dvictor357/blaze/tool"
	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func testTools() []adapter.Tool {
	return []adapter.Tool{
		adapter.NewTool("add", "Add two integers", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a": map[string]any{"type": "integer"},
				"b": map[string]any{"type": "integer"},
			},
			"required":             []string{"a", "b"},
			"additionalProperties": false,
		}, func(ctx context.Context, in json.RawMessage) (any, error) {
			var v struct{ A, B int }
			if err := json.Unmarshal(in, &v); err != nil {
				return nil, err
			}
			return map[string]int{"sum": v.A + v.B}, nil
		}),
		adapter.NewTool("boom", "Always panics", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
			panic("kaboom")
		}),
		adapter.NewTool("slow", "Waits for its deadline", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
		adapter.NewTool("fail", "Returns an error", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
			return nil, errors.New("upstream unavailable")
		}),
		tool.NewMemoryTool(tool.NewMemoryStore(tool.MemoryOptions{})),
		tool.NewWebFetchTool(nil),
	}
}

func newExecutor(t *testing.T) *adapter.Executor {
	t.Helper()
	ex, err := adapter.NewExecutor(adapter.Config{CallTimeout: 200 * time.Millisecond}, testTools()...)
	if err != nil {
		t.Fatal(err)
	}
	return ex
}

// connectInMemory runs the server on an in-memory transport and returns a
// connected client session from the official SDK client.
func connectInMemory(t *testing.T, ex *adapter.Executor, opts blazemcp.Options) *sdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	serverT, clientT := sdk.NewInMemoryTransports()
	srv := blazemcp.NewServer(ex, opts)
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cs.Close()
		ss.Wait()
		cancel()
	})
	return cs
}

func text(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("want 1 content block, got %d", len(res.Content))
	}
	tc, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("want text content, got %T", res.Content[0])
	}
	return tc.Text
}

func callTool(t *testing.T, cs *sdk.ClientSession, name string, args any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func errorCode(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	if !res.IsError {
		t.Fatalf("want an error result, got %s", text(t, res))
	}
	var body struct {
		Error adapter.CallError `json:"error"`
	}
	if err := json.Unmarshal([]byte(text(t, res)), &body); err != nil {
		t.Fatalf("error body is not JSON: %v", err)
	}
	return body.Error.Code
}

func TestListTools_ExposesEveryToolWithItsSchema(t *testing.T) {
	ex := newExecutor(t)
	cs := connectInMemory(t, ex, blazemcp.Options{Name: "test-server", Version: "1.2.3"})

	if got := cs.InitializeResult().ServerInfo; got.Name != "test-server" || got.Version != "1.2.3" {
		t.Fatalf("server info: %+v", got)
	}
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]adapter.Tool{}
	for _, tl := range ex.Tools() {
		want[tl.Name] = tl
	}
	if len(res.Tools) != len(want) {
		t.Fatalf("listed %d tools, want %d", len(res.Tools), len(want))
	}
	for _, got := range res.Tools { // the SDK lists tools sorted by name
		w, ok := want[got.Name]
		if !ok || got.Description != w.Description {
			t.Errorf("unexpected tool %s %q", got.Name, got.Description)
			continue
		}
		gotSchema, _ := json.Marshal(got.InputSchema)
		wantSchema, _ := json.Marshal(w.InputSchema)
		if string(gotSchema) != string(wantSchema) {
			t.Errorf("tool %s schema:\n got %s\nwant %s", got.Name, gotSchema, wantSchema)
		}
	}
}

func TestCallTool_SuccessReturnsTextAndStructuredContent(t *testing.T) {
	cs := connectInMemory(t, newExecutor(t), blazemcp.Options{})
	res := callTool(t, cs, "add", map[string]any{"a": 2, "b": 40})
	if res.IsError {
		t.Fatalf("unexpected error: %s", text(t, res))
	}
	if got := text(t, res); got != `{"sum":42}` {
		t.Fatalf("text = %s", got)
	}
	structured, _ := json.Marshal(res.StructuredContent)
	if string(structured) != `{"sum":42}` {
		t.Fatalf("structured content = %s", structured)
	}
}

func TestCallTool_FailuresAreToolErrorsTheModelCanSee(t *testing.T) {
	cs := connectInMemory(t, newExecutor(t), blazemcp.Options{})

	invalid := callTool(t, cs, "add", map[string]any{"a": "two", "c": 1})
	if code := errorCode(t, invalid); code != adapter.CodeInvalidArguments {
		t.Fatalf("code = %s", code)
	}
	for _, want := range []string{`missing required property \"b\"`, `unknown property \"c\"`, `$.a: expected integer, got string`} {
		if !strings.Contains(text(t, invalid), want) {
			t.Errorf("message %s missing %s", text(t, invalid), want)
		}
	}

	if code := errorCode(t, callTool(t, cs, "boom", nil)); code != adapter.CodePanic {
		t.Fatalf("panic code = %s", code)
	}
	if code := errorCode(t, callTool(t, cs, "slow", nil)); code != adapter.CodeTimeout {
		t.Fatalf("timeout code = %s", code)
	}
	failed := callTool(t, cs, "fail", nil)
	if code := errorCode(t, failed); code != adapter.CodeHandler || !strings.Contains(text(t, failed), "upstream unavailable") {
		t.Fatalf("handler error: %s", text(t, failed))
	}
	// The server survives all of the above.
	if res := callTool(t, cs, "add", map[string]any{"a": 1, "b": 1}); res.IsError {
		t.Fatalf("server unusable after failures: %s", text(t, res))
	}
}

func TestCallTool_UnknownToolIsAProtocolError(t *testing.T) {
	cs := connectInMemory(t, newExecutor(t), blazemcp.Options{})
	_, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "nope"})
	if err == nil {
		t.Fatal("want an error for an unknown tool")
	}
}

func TestCallTool_OversizedArgumentsAreRejectedBeforeRunning(t *testing.T) {
	ex, err := adapter.NewExecutor(adapter.Config{MaxInputBytes: 64}, testTools()...)
	if err != nil {
		t.Fatal(err)
	}
	cs := connectInMemory(t, ex, blazemcp.Options{})
	res := callTool(t, cs, "add", map[string]any{"a": 1, "b": 2, "pad": strings.Repeat("x", 100)})
	if code := errorCode(t, res); code != adapter.CodeInputTooLarge {
		t.Fatalf("code = %s", code)
	}
}

func TestCallTool_WebToolsKeepSSRFProtection(t *testing.T) {
	cs := connectInMemory(t, newExecutor(t), blazemcp.Options{})
	for _, target := range []string{"http://127.0.0.1:1/", "http://169.254.169.254/latest/meta-data/", "http://[::1]/"} {
		res := callTool(t, cs, "web_fetch", map[string]any{"url": target})
		if code := errorCode(t, res); code != adapter.CodeHandler {
			t.Fatalf("%s: code = %s (%s)", target, code, text(t, res))
		}
	}
}

func TestCallTool_MemoryRequiresAScopeFromTheContextHook(t *testing.T) {
	cs := connectInMemory(t, newExecutor(t), blazemcp.Options{})
	res := callTool(t, cs, "memory", map[string]any{"action": "set", "key": "k", "value": 1})
	if !res.IsError || !strings.Contains(text(t, res), "memory scope required") {
		t.Fatalf("memory must fail closed without a scope: %s", text(t, res))
	}

	hookErr := errors.New("caller not allowed")
	denied := connectInMemory(t, newExecutor(t), blazemcp.Options{
		Context: func(ctx context.Context, req *sdk.CallToolRequest) (context.Context, error) { return nil, hookErr },
	})
	if _, err := denied.CallTool(context.Background(), &sdk.CallToolParams{Name: "add", Arguments: map[string]int{"a": 1, "b": 1}}); err == nil ||
		!strings.Contains(err.Error(), "caller not allowed") {
		t.Fatalf("hook error must reject the call, got %v", err)
	}
}

// Streamable HTTP with bearer authentication: each token gets its own
// memory scope through the Context hook.
func newHTTPServer(t *testing.T, ex *adapter.Executor) *httptest.Server {
	t.Helper()
	tokens := map[string]string{"token-alice": "alice", "token-bob": "bob"}
	verify := func(ctx context.Context, token string, req *http.Request) (*auth.TokenInfo, error) {
		user, ok := tokens[token]
		if !ok {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: user, Expiration: time.Now().Add(time.Hour)}, nil
	}
	h := blazemcp.NewHTTPHandler(ex, blazemcp.Options{
		Context: func(ctx context.Context, req *sdk.CallToolRequest) (context.Context, error) {
			if req.Extra == nil || req.Extra.TokenInfo == nil || req.Extra.TokenInfo.UserID == "" {
				return nil, errors.New("unauthenticated")
			}
			return tool.WithMemoryScope(ctx, "user:"+req.Extra.TokenInfo.UserID), nil
		},
	})
	srv := httptest.NewServer(auth.RequireBearerToken(verify, nil)(h))
	t.Cleanup(srv.Close)
	return srv
}

type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func connectHTTP(t *testing.T, url, token string) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}, Timeout: 10 * time.Second},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestHTTP_PerUserMemoryScope(t *testing.T) {
	srv := newHTTPServer(t, newExecutor(t))
	alice := connectHTTP(t, srv.URL, "token-alice")
	bob := connectHTTP(t, srv.URL, "token-bob")

	if res := callTool(t, alice, "memory", map[string]any{"action": "set", "key": "secret", "value": "alice-only"}); res.IsError {
		t.Fatalf("set: %s", text(t, res))
	}
	got := text(t, callTool(t, alice, "memory", map[string]any{"action": "get", "key": "secret"}))
	if !strings.Contains(got, "alice-only") {
		t.Fatalf("alice cannot read her own value: %s", got)
	}
	got = text(t, callTool(t, bob, "memory", map[string]any{"action": "get", "key": "secret"}))
	if strings.Contains(got, "alice-only") {
		t.Fatalf("bob read alice's value: %s", got)
	}
	keys := text(t, callTool(t, bob, "memory", map[string]any{"action": "keys"}))
	if strings.Contains(keys, "secret") {
		t.Fatalf("bob can enumerate alice's keys: %s", keys)
	}
}

func postJSONRPC(t *testing.T, url, body string, header map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer token-alice")
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}`

func TestHTTP_TransportProtections(t *testing.T) {
	ex, err := adapter.NewExecutor(adapter.Config{MaxRequestBytes: 4 << 10}, testTools()...)
	if err != nil {
		t.Fatal(err)
	}
	srv := newHTTPServer(t, ex)

	if resp := postJSONRPC(t, srv.URL, initializeBody, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("baseline initialize: status %d", resp.StatusCode)
	}

	t.Run("missing token", func(t *testing.T) {
		resp := postJSONRPC(t, srv.URL, initializeBody, map[string]string{"Authorization": "Bearer wrong"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
	t.Run("DNS rebinding", func(t *testing.T) {
		resp := postJSONRPC(t, srv.URL, initializeBody, map[string]string{"Host": "attacker.example"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
	t.Run("cross-origin browser request", func(t *testing.T) {
		resp := postJSONRPC(t, srv.URL, initializeBody, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://attacker.example"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
	t.Run("body limit", func(t *testing.T) {
		big := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"add","arguments":{"a":1,"b":2,"pad":"` + strings.Repeat("x", 8<<10) + `"}}}`
		resp := postJSONRPC(t, srv.URL, big, nil)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
	t.Run("no long-lived GET stream", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		req.Header.Set("Authorization", "Bearer token-alice")
		req.Header.Set("Accept", "text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
}
