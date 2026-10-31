package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dvictor357/blaze/adapter"
)

// The built-in schemas must compile for runtime validation, and invalid
// arguments must be rejected before any built-in handler runs.
func TestBuiltinSchemasAreEnforced(t *testing.T) {
	web := NewWebClient(WebClientConfig{})
	defer web.Close()
	ex, err := adapter.NewExecutor(adapter.Config{},
		NewWebSearchTool(web), NewWebReadTool(web), NewWebFetchTool(web),
		NewDateTimeTool(), NewJSONQueryTool(), NewMemoryTool(nil))
	if err != nil {
		t.Fatalf("built-in schemas must compile: %v", err)
	}
	ctx := WithMemoryScope(context.Background(), "test")
	results, err := ex.Execute(ctx, []adapter.Call{
		{ID: "1", Name: "datetime", Input: json.RawMessage(`{"action":"yesterday"}`)},
		{ID: "2", Name: "memory", Input: json.RawMessage(`{"action":"get","key":"k","namespace":"other"}`)},
		{ID: "3", Name: "json_query", Input: json.RawMessage(`{"json":"{}"}`)},
		{ID: "4", Name: "web_fetch", Input: json.RawMessage(`{"url":42}`)},
		{ID: "5", Name: "datetime", Input: json.RawMessage(`{"action":"now"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	wants := []string{`$.action: must be one of`, `unknown property "namespace"`, `missing required property "query"`, `$.url: expected string, got integer`}
	for i, want := range wants {
		r := results[i]
		if r.OK || r.Error.Code != adapter.CodeInvalidArguments || !strings.Contains(r.Error.Message, want) {
			t.Errorf("call %s: want invalid_arguments containing %q, got %+v", r.ID, want, r)
		}
	}
	if !results[4].OK {
		t.Errorf("valid datetime call failed: %+v", results[4])
	}
}
