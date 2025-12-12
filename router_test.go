package blaze

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func BenchmarkRouter_Static(b *testing.B) {
	r := newRouter()
	for i := range 100 {
		r.handle("GET", fmt.Sprintf("/path%d/to/resource", i), func(c *Context) error { return nil })
	}

	for b.Loop() {
		r.lookup("GET", "/path50/to/resource")
	}
}

func BenchmarkRouter_Param(b *testing.B) {
	r := newRouter()
	r.handle("GET", "/users/:id", func(c *Context) error { return nil })
	r.handle("GET", "/users/:id/posts/:postId", func(c *Context) error { return nil })

	for b.Loop() {
		r.lookup("GET", "/users/123/posts/456")
	}
}

func BenchmarkRouter_Mixed(b *testing.B) {
	r := newRouter()
	// Add 100 static routes
	for i := range 100 {
		r.handle("GET", fmt.Sprintf("/api/v1/resource%d", i), func(c *Context) error { return nil })
	}
	// Add param routes
	r.handle("GET", "/users/:id", func(c *Context) error { return nil })
	r.handle("POST", "/users/:id", func(c *Context) error { return nil })
	r.handle("GET", "/products/:category/:id", func(c *Context) error { return nil })

	for b.Loop() {
		r.lookup("GET", "/users/999")
		r.lookup("GET", "/api/v1/resource50")
		r.lookup("GET", "/products/electronics/42")
	}
}

func TestRouter_Basic(t *testing.T) {
	r := newRouter()
	r.handle("GET", "/hello", func(c *Context) error {
		return nil
	})

	handler, params := r.lookup("GET", "/hello")
	if handler == nil {
		t.Fatal("handler not found")
	}
	if len(params) != 0 {
		t.Fatalf("expected 0 params, got %d", len(params))
	}
}

func TestRouter_Params(t *testing.T) {
	r := newRouter()
	r.handle("GET", "/users/:id", func(c *Context) error { return nil })

	handler, params := r.lookup("GET", "/users/123")
	if handler == nil {
		t.Fatal("handler not found")
	}
	if params["id"] != "123" {
		t.Fatalf("expected id=123, got %s", params["id"])
	}
}

func TestRouter_MultipleParams(t *testing.T) {
	r := newRouter()
	r.handle("GET", "/users/:userId/posts/:postId", func(c *Context) error { return nil })

	handler, params := r.lookup("GET", "/users/42/posts/99")
	if handler == nil {
		t.Fatal("handler not found")
	}
	if params["userId"] != "42" {
		t.Fatalf("expected userId=42, got %s", params["userId"])
	}
	if params["postId"] != "99" {
		t.Fatalf("expected postId=99, got %s", params["postId"])
	}
}

func TestRouter_Wildcard(t *testing.T) {
	r := newRouter()
	r.handle("GET", "/files/*filepath", func(c *Context) error { return nil })

	handler, params := r.lookup("GET", "/files/foo/bar/baz.txt")
	if handler == nil {
		t.Fatal("handler not found")
	}
	if params["filepath"] != "foo/bar/baz.txt" {
		t.Fatalf("expected filepath=foo/bar/baz.txt, got %s", params["filepath"])
	}
}

func TestRouter_Methods(t *testing.T) {
	r := newRouter()
	r.handle("GET", "/resource", func(c *Context) error { return nil })
	r.handle("POST", "/resource", func(c *Context) error { return nil })

	getHandler, _ := r.lookup("GET", "/resource")
	postHandler, _ := r.lookup("POST", "/resource")
	deleteHandler, _ := r.lookup("DELETE", "/resource")

	if getHandler == nil {
		t.Fatal("GET handler not found")
	}
	if postHandler == nil {
		t.Fatal("POST handler not found")
	}
	if deleteHandler != nil {
		t.Fatal("DELETE handler should not exist")
	}
}

func TestRouter_ServeHTTP(t *testing.T) {
	r := newRouter()
	r.handle("GET", "/test", func(c *Context) error {
		return c.String(200, "OK")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != "OK" {
		t.Fatalf("expected OK, got %s", w.Body.String())
	}
}

func TestRouter_NotFound(t *testing.T) {
	r := newRouter()

	req := httptest.NewRequest("GET", "/notexist", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
