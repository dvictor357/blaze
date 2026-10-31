package blaze

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func withOrigin(origin string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Origin", origin) }
}

func preflight(origin, method string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", method)
	}
}

func corsEngine(cfg ...CORSConfig) (*Engine, *int) {
	calls := 0
	e := New()
	e.Wrap(CORS(cfg...))
	e.POST("/rpc", func(c *Context) error {
		calls++
		return c.String(200, "done")
	})
	return e, &calls
}

func TestCORS_PreflightIsAnsweredWithoutAnOptionsRoute(t *testing.T) {
	e, calls := corsEngine(CORSConfig{
		AllowOrigins: []string{"https://app.example"},
		AllowMethods: []string{"POST"},
		AllowHeaders: []string{"Content-Type"},
		MaxAge:       10 * time.Minute,
	})

	rec := do(e, "OPTIONS", "/rpc", preflight("https://app.example", "POST"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight = %d, want 204", rec.Code)
	}
	h := rec.Header()
	if h.Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Errorf("allow-origin = %q", h.Get("Access-Control-Allow-Origin"))
	}
	if h.Get("Access-Control-Allow-Methods") != "POST" || h.Get("Access-Control-Allow-Headers") != "Content-Type" {
		t.Errorf("allow methods/headers = %q / %q", h.Get("Access-Control-Allow-Methods"), h.Get("Access-Control-Allow-Headers"))
	}
	if h.Get("Access-Control-Max-Age") != "600" {
		t.Errorf("max-age = %q, want 600", h.Get("Access-Control-Max-Age"))
	}
	if !strings.Contains(strings.Join(h.Values("Vary"), ","), "Origin") {
		t.Errorf("Vary = %v, want Origin", h.Values("Vary"))
	}
	if *calls != 0 {
		t.Errorf("preflight must not run the route handler (ran %d times)", *calls)
	}
}

func TestCORS_ActualRequest(t *testing.T) {
	e, calls := corsEngine(CORSConfig{AllowOrigins: []string{"https://app.example"}})

	rec := do(e, "POST", "/rpc", withOrigin("https://app.example"))
	if rec.Code != 200 || *calls != 1 {
		t.Fatalf("POST = %d after %d handler calls", rec.Code, *calls)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Errorf("allow-origin = %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Origin") {
		t.Errorf("Vary = %v, want Origin", rec.Header().Values("Vary"))
	}
}

func TestCORS_DisallowedOriginGetsNoHeadersAndNormalRouting(t *testing.T) {
	e, calls := corsEngine(CORSConfig{AllowOrigins: []string{"https://app.example"}})

	rec := do(e, "OPTIONS", "/rpc", preflight("https://evil.example", "POST"))
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("disallowed origin received allow-origin %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("disallowed preflight falls through to normal routing: got %d, want 405", rec.Code)
	}
	if !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Origin") {
		t.Errorf("responses that depend on Origin must say so; Vary = %v", rec.Header().Values("Vary"))
	}

	rec = do(e, "POST", "/rpc", withOrigin("https://evil.example"))
	if rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Code != 200 || *calls != 1 {
		t.Errorf("simple request from a disallowed origin: %d, allow-origin %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORS_WildcardAndNoOrigin(t *testing.T) {
	e, _ := corsEngine()

	rec := do(e, "POST", "/rpc", withOrigin("https://anything.example"))
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("default config allow-origin = %q, want *", rec.Header().Get("Access-Control-Allow-Origin"))
	}

	rec = do(e, "POST", "/rpc")
	if rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Vary") != "" {
		t.Errorf("same-origin request must be untouched: %v", rec.Header())
	}
}

func TestCORS_OptionsWithoutRequestMethodIsNotAPreflight(t *testing.T) {
	e, _ := corsEngine(CORSConfig{AllowOrigins: []string{"*"}})

	rec := do(e, "OPTIONS", "/rpc", withOrigin("https://app.example"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("plain OPTIONS = %d, want 405 from normal routing", rec.Code)
	}
}

func TestWrap_OrderAndFreeze(t *testing.T) {
	var trace []string
	layer := func(name string) HTTPMiddleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				trace = append(trace, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	e := New()
	e.Wrap(layer("first"), layer("second"))
	e.GET("/x", func(c *Context) error { return c.String(200, "ok") })
	do(e, "GET", "/x")
	if got := strings.Join(trace, ","); got != "first,second" {
		t.Errorf("wrap order = %s, want first,second", got)
	}

	mustPanic(t, "Wrap after serving", func() { e.Wrap(layer("late")) })
}

func TestWrap_SeesRequestsThatMatchNoRoute(t *testing.T) {
	e := New()
	seen := ""
	e.Wrap(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.URL.Path
			next.ServeHTTP(w, r)
		})
	})

	if rec := do(e, "GET", "/nowhere"); rec.Code != http.StatusNotFound || seen != "/nowhere" {
		t.Errorf("unmatched request = %d, wrapper saw %q", rec.Code, seen)
	}
}
