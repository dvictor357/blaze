package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dvictor357/blaze/tool"
)

// fakeModel answers the first request with a tool call and the second with
// text, recording each request body.
func fakeModel(t *testing.T, first, second string) (*httptest.Server, *[]string) {
	t.Helper()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			io.WriteString(w, first)
		} else {
			io.WriteString(w, second)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func ctx() context.Context { return tool.WithMemoryScope(context.Background(), "test") }

func TestOpenAILoopOffline(t *testing.T) {
	srv, bodies := fakeModel(t,
		`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"memory","arguments":"{\"action\":\"set\",\"key\":\"admin_email\",\"value\":\"bo@example.com\"}"}}]}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Saved."}}]}`)
	openAIURL = srv.URL
	ex, err := newExecutor()
	if err != nil {
		t.Fatal(err)
	}
	answer, err := runOpenAI(ctx(), ex, "test-key", "test-model")
	if err != nil || answer != "Saved." {
		t.Fatalf("answer %q, err %v", answer, err)
	}
	second := (*bodies)[1]
	if !strings.Contains(second, `"tool_call_id":"c1"`) || !strings.Contains(second, `\"success\":true`) {
		t.Fatalf("tool result not sent back: %s", second)
	}
}

func TestAnthropicLoopOffline(t *testing.T) {
	srv, bodies := fakeModel(t,
		`{"stop_reason":"tool_use","content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"tool_use","id":"tu1","name":"datetime","input":{"action":"now","timezone":"Asia/Tokyo"}}]}`,
		`{"stop_reason":"end_turn","content":[{"type":"text","text":"It is late in Tokyo."}]}`)
	anthropicURL = srv.URL
	ex, err := newExecutor()
	if err != nil {
		t.Fatal(err)
	}
	answer, err := runAnthropic(ctx(), ex, "test-key", "test-model")
	if err != nil || answer != "It is late in Tokyo." {
		t.Fatalf("answer %q, err %v", answer, err)
	}
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte((*bodies)[1]), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 3 || req.Messages[1].Role != "assistant" || req.Messages[2].Role != "user" {
		t.Fatalf("unexpected history: %s", (*bodies)[1])
	}
	if !strings.Contains(string(req.Messages[1].Content), `"signature":"sig"`) {
		t.Fatalf("assistant content (thinking block) must be sent back unchanged: %s", req.Messages[1].Content)
	}
	if !strings.Contains(string(req.Messages[2].Content), `"tool_use_id":"tu1"`) {
		t.Fatalf("tool_result missing: %s", req.Messages[2].Content)
	}
}
