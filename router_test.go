package blaze

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// do sends one request through h and returns the recorded response.
func do(h http.Handler, method, target string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for _, m := range mutate {
		m(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func mustPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected a panic", name)
		}
	}()
	fn()
}

func TestEngine_StaticParamPrecedenceAndBacktracking(t *testing.T) {
	e := New()
	e.GET("/users/new", func(c *Context) error { return c.String(200, "static") })
	e.GET("/users/{id}", func(c *Context) error { return c.String(200, "user:"+c.Param("id")) })
	e.GET("/users/{id}/posts", func(c *Context) error { return c.String(200, "posts:"+c.Param("id")) })
	e.GET("/a/{id}/x", func(c *Context) error { return c.String(200, "param:"+c.Param("id")) })
	e.GET("/a/static/y", func(c *Context) error { return c.String(200, "static-y") })

	tests := map[string]string{
		"/users/new":       "static",
		"/users/42":        "user:42",
		"/users/new/posts": "posts:new", // a static sibling must not hide the param route
		"/users/42/posts":  "posts:42",
		"/a/static/x":      "param:static", // falls back from the static branch
		"/a/static/y":      "static-y",
	}
	for path, want := range tests {
		rec := do(e, "GET", path)
		if rec.Code != 200 || rec.Body.String() != want {
			t.Errorf("GET %s = %d %q, want 200 %q", path, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestEngine_ParamNamesBelongToTheirOwnRoute(t *testing.T) {
	e := New()
	e.GET("/teams/{id}", func(c *Context) error { return c.String(200, "id="+c.Param("id")) })
	e.GET("/teams/{teamID}/members", func(c *Context) error { return c.String(200, "teamID="+c.Param("teamID")) })

	if got := do(e, "GET", "/teams/42").Body.String(); got != "id=42" {
		t.Errorf("/teams/42 = %q", got)
	}
	if got := do(e, "GET", "/teams/42/members").Body.String(); got != "teamID=42" {
		t.Errorf("/teams/42/members = %q", got)
	}
}

func TestEngine_Wildcard(t *testing.T) {
	e := New()
	e.GET("/files/{path...}", func(c *Context) error { return c.String(200, "["+c.Param("path")+"]") })

	tests := map[string]string{
		"/files/foo/bar/baz.txt": "[foo/bar/baz.txt]",
		"/files/one":             "[one]",
		"/files/":                "[]", // zero-length tail
	}
	for path, want := range tests {
		if got := do(e, "GET", path).Body.String(); got != want {
			t.Errorf("GET %s = %q, want %q", path, got, want)
		}
	}
}

func TestEngine_PathEscapingIsDecodedInParams(t *testing.T) {
	e := New()
	e.GET("/users/{id}", func(c *Context) error { return c.String(200, c.Param("id")) })

	if got := do(e, "GET", "/users/a%2Fb").Body.String(); got != "a/b" {
		t.Errorf("escaped slash = %q, want %q", got, "a/b")
	}
	if got := do(e, "GET", "/users/caf%C3%A9").Body.String(); got != "café" {
		t.Errorf("escaped utf-8 = %q, want %q", got, "café")
	}
}

func TestEngine_RootPatternMatchesOnlyRoot(t *testing.T) {
	e := New()
	e.GET("/{$}", func(c *Context) error { return c.String(200, "root") })

	if rec := do(e, "GET", "/"); rec.Code != 200 {
		t.Errorf("GET / = %d", rec.Code)
	}
	if rec := do(e, "GET", "/other"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /other = %d, want 404", rec.Code)
	}
}

func TestEngine_MethodBehavior(t *testing.T) {
	e := New()
	e.POST("/resource", func(c *Context) error { return c.String(200, "created") })
	e.GET("/page", func(c *Context) error { return c.String(200, "page") })

	rec := do(e, "GET", "/resource")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on POST-only route = %d, want 405", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Allow"), "POST") {
		t.Errorf("Allow header = %q, want it to list POST", rec.Header().Get("Allow"))
	}

	if rec := do(e, "GET", "/missing"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown path = %d, want 404", rec.Code)
	}

	if head := do(e, "HEAD", "/page"); head.Code != 200 {
		t.Errorf("HEAD on a GET route = %d, want 200", head.Code)
	}
}

func TestEngine_ConflictingAndInvalidRegistrationsPanic(t *testing.T) {
	h := func(c *Context) error { return nil }

	mustPanic(t, "same pattern twice", func() {
		e := New()
		e.GET("/x", h)
		e.GET("/x", h)
	})
	mustPanic(t, "same shape, different param names", func() {
		e := New()
		e.GET("/x/{a}", h)
		e.GET("/x/{b}", h)
	})
	mustPanic(t, "ambiguous overlap", func() {
		e := New()
		e.GET("/a/{x}/b", h)
		e.GET("/a/b/{y}", h)
	})
	mustPanic(t, "pattern without leading slash", func() {
		e := New()
		e.GET("users/{id}", h)
	})
	mustPanic(t, "nil handler", func() { New().GET("/x", nil) })
	mustPanic(t, "empty method", func() { New().Handle("", "/x", h) })
	mustPanic(t, "group prefix with trailing slash", func() { New().Group("/api/") })
	mustPanic(t, "group prefix without leading slash", func() { New().Group("api") })

	// Same path with different methods is fine.
	e := New()
	e.GET("/x", h)
	e.POST("/x", h)
}

func TestEngine_MiddlewareOrderAndGroupInheritance(t *testing.T) {
	var trace []string
	mw := func(name string) MiddlewareFunc {
		return func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				trace = append(trace, name)
				return next(c)
			}
		}
	}

	e := New()
	e.Use(mw("engine1"), mw("engine2"))
	api := e.Group("/api")
	api.Use(mw("api"))
	v1 := api.Group("/v1")
	v1.Use(mw("v1"))
	v1.GET("/ping", func(c *Context) error {
		trace = append(trace, "handler")
		return c.String(200, "pong")
	})
	api.GET("/plain", func(c *Context) error {
		trace = append(trace, "plain")
		return nil
	})

	if rec := do(e, "GET", "/api/v1/ping"); rec.Body.String() != "pong" {
		t.Fatalf("response = %q", rec.Body.String())
	}
	want := "engine1,engine2,api,v1,handler"
	if got := strings.Join(trace, ","); got != want {
		t.Errorf("nested group order = %s, want %s", got, want)
	}

	// The parent group's own routes do not run the child group's middleware.
	trace = nil
	do(e, "GET", "/api/plain")
	want = "engine1,engine2,api,plain"
	if got := strings.Join(trace, ","); got != want {
		t.Errorf("parent group order = %s, want %s", got, want)
	}
}

func TestEngine_ErrorHandling(t *testing.T) {
	e := New()
	e.GET("/secret", func(c *Context) error { return errors.New("db password=hunter2") })
	e.GET("/http", func(c *Context) error { return &HTTPError{Status: http.StatusTeapot, Message: "short and stout"} })
	e.GET("/partial", func(c *Context) error {
		if err := c.String(200, "partial"); err != nil {
			return err
		}
		return errors.New("failed after the response started")
	})

	rec := do(e, "GET", "/secret")
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("internal error leaked or wrong status: %d %q", rec.Code, rec.Body.String())
	}

	rec = do(e, "GET", "/http")
	if rec.Code != http.StatusTeapot || rec.Body.String() != "short and stout" {
		t.Errorf("HTTPError = %d %q", rec.Code, rec.Body.String())
	}

	rec = do(e, "GET", "/partial")
	if rec.Code != 200 || rec.Body.String() != "partial" {
		t.Errorf("error after commit must not alter the response: %d %q", rec.Code, rec.Body.String())
	}
}

func TestEngine_CustomErrorHandler(t *testing.T) {
	e := New()
	e.ErrorHandler = func(c *Context, err error) {
		_ = c.JSON(http.StatusBadGateway, map[string]string{"error": "custom"})
	}
	e.GET("/x", func(c *Context) error { return errors.New("boom") })

	rec := do(e, "GET", "/x")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"custom"`) {
		t.Errorf("custom handler not used: %d %q", rec.Code, rec.Body.String())
	}
}

func TestEngine_CanceledMiddlewareStillRendersErrors(t *testing.T) {
	for _, kind := range []string{"internal", "http", "custom"} {
		t.Run(kind, func(t *testing.T) {
			e := New()
			e.Use(func(next HandlerFunc) HandlerFunc {
				return func(c *Context) error {
					ctx, cancel := context.WithCancel(c.Request.Context())
					defer cancel()
					c.Request = c.Request.WithContext(ctx)
					return next(c)
				}
			})
			wantStatus := http.StatusInternalServerError
			if kind == "custom" {
				wantStatus = http.StatusBadGateway
				e.ErrorHandler = func(c *Context, err error) {
					_ = c.JSON(http.StatusBadGateway, map[string]bool{"failed": true})
				}
			}
			if kind == "http" {
				wantStatus = http.StatusUnauthorized
			}
			e.GET("/error", func(c *Context) error {
				if kind == "http" {
					return &HTTPError{Status: http.StatusUnauthorized, Message: "unauthorized"}
				}
				return errors.New("private handler failure")
			})
			rec := do(e, http.MethodGet, "/error")
			if rec.Code != wantStatus {
				t.Fatalf("status = %d, want %d; body = %q", rec.Code, wantStatus, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "private handler failure") {
				t.Fatal("internal error text leaked")
			}
		})
	}
}

func TestRecovery(t *testing.T) {
	e := New()
	e.Use(Recovery())
	e.GET("/boom", func(c *Context) error { panic("kaboom") })
	e.GET("/late", func(c *Context) error {
		_ = c.String(200, "partial")
		panic("kaboom after write")
	})

	rec := do(e, "GET", "/boom")
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "kaboom") {
		t.Errorf("panic response = %d %q, want a generic 500", rec.Code, rec.Body.String())
	}

	rec = do(e, "GET", "/late")
	if rec.Code != 200 || rec.Body.String() != "partial" {
		t.Errorf("panic after commit must not append to the response: %d %q", rec.Code, rec.Body.String())
	}
}

// discardWriter is a minimal ResponseWriter for benchmarks.
type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header         { return d.h }
func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (d *discardWriter) WriteHeader(int)             {}

// The engine benchmarks measure a full in-process ServeHTTP dispatch (mux
// match, Context allocation, handler call). They are useful to compare
// changes to this package against each other, not to quote as server
// throughput: there is no network, TLS or real handler work involved.

func benchServe(b *testing.B, e *Engine, paths ...string) {
	reqs := make([]*http.Request, len(paths))
	for i, p := range paths {
		reqs[i] = httptest.NewRequest("GET", p, nil)
	}
	w := &discardWriter{h: http.Header{}}
	b.ReportAllocs()
	for b.Loop() {
		for _, req := range reqs {
			e.ServeHTTP(w, req)
		}
	}
}

func BenchmarkEngine_Static(b *testing.B) {
	e := New()
	for i := range 100 {
		e.GET(fmt.Sprintf("/path%d/to/resource", i), func(c *Context) error { return nil })
	}
	benchServe(b, e, "/path50/to/resource")
}

func BenchmarkEngine_Param(b *testing.B) {
	e := New()
	e.GET("/users/{id}", func(c *Context) error { return nil })
	e.GET("/users/{id}/posts/{postId}", func(c *Context) error { return nil })
	benchServe(b, e, "/users/123/posts/456")
}

func BenchmarkEngine_Mixed(b *testing.B) {
	e := New()
	for i := range 100 {
		e.GET(fmt.Sprintf("/api/v1/resource%d", i), func(c *Context) error { return nil })
	}
	e.GET("/users/{id}", func(c *Context) error { return nil })
	e.POST("/users/{id}", func(c *Context) error { return nil })
	e.GET("/products/{category}/{id}", func(c *Context) error { return nil })
	benchServe(b, e, "/users/999", "/api/v1/resource50", "/products/electronics/42")
}
