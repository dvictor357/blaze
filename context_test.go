package blaze

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postJSON(h http.Handler, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type bindTarget struct {
	Name string `json:"name"`
}

func bindEngine(bind func(c *Context, v *bindTarget) error) *Engine {
	e := New()
	e.POST("/bind", func(c *Context) error {
		var v bindTarget
		if err := bind(c, &v); err != nil {
			return err
		}
		return c.String(200, "name="+v.Name)
	})
	return e
}

func TestBindJSON(t *testing.T) {
	e := bindEngine(func(c *Context, v *bindTarget) error { return c.BindJSON(v) })

	if rec := postJSON(e, "/bind", `{"name":"blaze"}`); rec.Code != 200 || rec.Body.String() != "name=blaze" {
		t.Errorf("valid body = %d %q", rec.Code, rec.Body.String())
	}
	// Unknown fields are tolerated by the lenient binder.
	if rec := postJSON(e, "/bind", `{"name":"x","extra":1}`); rec.Code != 200 {
		t.Errorf("unknown field = %d, want 200", rec.Code)
	}
	if rec := postJSON(e, "/bind", `{not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body = %d, want 400", rec.Code)
	}
	if rec := postJSON(e, "/bind", `{"name":"a"}{"name":"b"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("two JSON values = %d, want 400", rec.Code)
	}
	if rec := postJSON(e, "/bind", `{"name":"a"} garbage`); rec.Code != http.StatusBadRequest {
		t.Errorf("trailing garbage = %d, want 400", rec.Code)
	}
}

func TestBindJSON_BodyLimit(t *testing.T) {
	e := bindEngine(func(c *Context, v *bindTarget) error { return c.BindJSONLimit(v, 32) })

	if rec := postJSON(e, "/bind", `{"name":"ok"}`); rec.Code != 200 {
		t.Errorf("small body = %d, want 200", rec.Code)
	}
	big := `{"name":"` + strings.Repeat("x", 100) + `"}`
	if rec := postJSON(e, "/bind", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d, want 413", rec.Code)
	}
}

func TestBindJSON_DefaultLimitApplies(t *testing.T) {
	e := bindEngine(func(c *Context, v *bindTarget) error { return c.BindJSON(v) })

	big := `{"name":"` + strings.Repeat("x", int(DefaultMaxBodyBytes)) + `"}`
	if rec := postJSON(e, "/bind", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("body over DefaultMaxBodyBytes = %d, want 413", rec.Code)
	}
}

func TestBindJSONStrict_RejectsUnknownFields(t *testing.T) {
	e := bindEngine(func(c *Context, v *bindTarget) error { return c.BindJSONStrict(v, 1024) })

	if rec := postJSON(e, "/bind", `{"name":"x"}`); rec.Code != 200 {
		t.Errorf("known fields = %d, want 200", rec.Code)
	}
	if rec := postJSON(e, "/bind", `{"name":"x","messages":[]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400", rec.Code)
	}
}

func TestJSON_EncodeFailureDoesNotCommitResponse(t *testing.T) {
	e := New()
	e.GET("/bad", func(c *Context) error { return c.JSON(200, map[string]any{"ch": make(chan int)}) })
	e.GET("/ok", func(c *Context) error { return c.JSON(201, map[string]int{"n": 1}) })

	rec := do(e, "GET", "/bad")
	if rec.Code != 500 || strings.Contains(rec.Header().Get("Content-Type"), "json") {
		t.Errorf("unencodable value = %d %q, want a clean 500", rec.Code, rec.Header().Get("Content-Type"))
	}

	rec = do(e, "GET", "/ok")
	if rec.Code != 201 || rec.Header().Get("Content-Type") != "application/json" || strings.TrimSpace(rec.Body.String()) != `{"n":1}` {
		t.Errorf("JSON response = %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestContext_QueryHelpers(t *testing.T) {
	e := New()
	e.GET("/q", func(c *Context) error {
		return c.String(200, c.Query("a")+"|"+c.QueryDefault("b", "fallback")+"|"+c.QueryDefault("a", "fallback"))
	})
	if got := do(e, "GET", "/q?a=1").Body.String(); got != "1|fallback|1" {
		t.Errorf("query helpers = %q", got)
	}
}
