package blaze

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// HandlerFunc defines the handler signature with error return
type HandlerFunc func(*Context) error

// MiddlewareFunc defines the per-route middleware signature. It wraps a
// HandlerFunc at registration time and runs only for matched routes.
type MiddlewareFunc func(HandlerFunc) HandlerFunc

// HTTPMiddleware wraps the whole engine, before routing and method dispatch.
// Use it (through Engine.Wrap) for concerns that must also see unmatched
// requests and preflights, such as CORS.
type HTTPMiddleware func(http.Handler) http.Handler

// Default server timeouts applied by Engine.Server and Engine.Listen.
// Handlers that legitimately run longer (streaming, long tool batches) extend
// their own write deadline with Context.SetWriteDeadline.
const (
	DefaultReadHeaderTimeout = 5 * time.Second
	DefaultReadTimeout       = 30 * time.Second
	DefaultWriteTimeout      = 60 * time.Second
	DefaultIdleTimeout       = 120 * time.Second
	DefaultMaxHeaderBytes    = 64 << 10
)

// Engine is the core framework instance. Routing is delegated to the standard
// library http.ServeMux, so route patterns use its syntax:
//
//	/users/{id}        single-segment parameter
//	/files/{path...}   remaining path (may be empty)
//	/{$}               exactly "/"
//
// Static segments take precedence over parameters, and registering two
// patterns that overlap ambiguously (or the same pattern and method twice)
// panics at registration time.
type Engine struct {
	// ErrorHandler renders an error returned by a handler before any response
	// bytes were written. The default maps *HTTPError to its status and
	// message and answers 500 "Internal Server Error" for everything else,
	// without leaking the error text. Set it before serving.
	ErrorHandler func(*Context, error)

	mux        *http.ServeMux
	middleware []MiddlewareFunc
	outer      []HTTPMiddleware

	build   sync.Once
	started atomic.Bool
	handler http.Handler
}

// New creates a new Engine instance
func New() *Engine {
	return &Engine{mux: http.NewServeMux()}
}

// Use adds global per-route middleware. It applies to routes registered after
// the call; the first Use call is the outermost middleware.
func (e *Engine) Use(middleware ...MiddlewareFunc) {
	e.middleware = append(e.middleware, middleware...)
}

// Wrap adds HTTP middleware around the entire engine (outside routing). The
// first Wrap call is the outermost layer. It panics if the engine already
// served a request.
func (e *Engine) Wrap(middleware ...HTTPMiddleware) {
	if e.started.Load() {
		panic("blaze: Wrap called after the engine started serving")
	}
	e.outer = append(e.outer, middleware...)
}

// Handle registers a route with any HTTP method. A GET route also answers
// HEAD, as with net/http.
func (e *Engine) Handle(method, pattern string, handler HandlerFunc) {
	e.register(method, pattern, handler, nil)
}

// HTTP method shortcuts
func (e *Engine) GET(path string, h HandlerFunc)     { e.Handle("GET", path, h) }
func (e *Engine) POST(path string, h HandlerFunc)    { e.Handle("POST", path, h) }
func (e *Engine) PUT(path string, h HandlerFunc)     { e.Handle("PUT", path, h) }
func (e *Engine) DELETE(path string, h HandlerFunc)  { e.Handle("DELETE", path, h) }
func (e *Engine) PATCH(path string, h HandlerFunc)   { e.Handle("PATCH", path, h) }
func (e *Engine) OPTIONS(path string, h HandlerFunc) { e.Handle("OPTIONS", path, h) }
func (e *Engine) HEAD(path string, h HandlerFunc)    { e.Handle("HEAD", path, h) }

// Group creates a new route group with a shared prefix. The prefix must be
// empty or start with "/" and must not end with "/".
func (e *Engine) Group(prefix string) *Group {
	return &Group{engine: e, prefix: checkPrefix(prefix)}
}

// Server returns an http.Server serving this engine with safe default
// timeouts. Adjust the fields (TLSConfig, timeouts, BaseContext...) before
// calling ListenAndServe or Serve.
func (e *Engine) Server(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           e,
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
		ReadTimeout:       DefaultReadTimeout,
		WriteTimeout:      DefaultWriteTimeout,
		IdleTimeout:       DefaultIdleTimeout,
		MaxHeaderBytes:    DefaultMaxHeaderBytes,
	}
}

// Listen starts the HTTP server with the default timeouts of Server.
func (e *Engine) Listen(addr string) error {
	log.Printf("Blaze running on %s", addr)
	return e.Server(addr).ListenAndServe()
}

// ServeHTTP implements http.Handler
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.build.Do(func() {
		var h http.Handler = e.mux
		for i := len(e.outer) - 1; i >= 0; i-- {
			h = e.outer[i](h)
		}
		e.handler = h
		e.started.Store(true)
	})
	e.handler.ServeHTTP(w, r)
}

// Group represents a route group with a shared prefix and middleware
type Group struct {
	engine     *Engine
	prefix     string
	middleware []MiddlewareFunc
}

// Use adds middleware to this group. Group middleware runs inside engine
// middleware and applies to routes registered after the call.
func (g *Group) Use(middleware ...MiddlewareFunc) {
	g.middleware = append(g.middleware, middleware...)
}

// Handle registers a route within the group
func (g *Group) Handle(method, path string, handler HandlerFunc) {
	g.engine.register(method, g.prefix+path, handler, g.middleware)
}

// HTTP method shortcuts for Group
func (g *Group) GET(path string, h HandlerFunc)     { g.Handle("GET", path, h) }
func (g *Group) POST(path string, h HandlerFunc)    { g.Handle("POST", path, h) }
func (g *Group) PUT(path string, h HandlerFunc)     { g.Handle("PUT", path, h) }
func (g *Group) DELETE(path string, h HandlerFunc)  { g.Handle("DELETE", path, h) }
func (g *Group) PATCH(path string, h HandlerFunc)   { g.Handle("PATCH", path, h) }
func (g *Group) OPTIONS(path string, h HandlerFunc) { g.Handle("OPTIONS", path, h) }
func (g *Group) HEAD(path string, h HandlerFunc)    { g.Handle("HEAD", path, h) }

// Group creates a nested group that inherits a copy of this group's
// middleware at creation time.
func (g *Group) Group(prefix string) *Group {
	return &Group{
		engine:     g.engine,
		prefix:     g.prefix + checkPrefix(prefix),
		middleware: append([]MiddlewareFunc{}, g.middleware...),
	}
}

func checkPrefix(prefix string) string {
	if prefix != "" && (!strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/")) {
		panic(fmt.Sprintf("blaze: invalid group prefix %q: must be empty or start with \"/\" and not end with \"/\"", prefix))
	}
	return prefix
}
