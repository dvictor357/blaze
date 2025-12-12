package blaze

import (
	"log"
	"net/http"
)

// HandlerFunc defines the handler signature with error return
type HandlerFunc func(*Context) error

// MiddlewareFunc defines the middleware signature
type MiddlewareFunc func(HandlerFunc) HandlerFunc

// Engine is the core framework instance
type Engine struct {
	router     *Router
	middleware []MiddlewareFunc
}

// New creates a new Engine instance
func New() *Engine {
	return &Engine{
		router: newRouter(),
	}
}

// Use adds global middleware
func (e *Engine) Use(middleware ...MiddlewareFunc) {
	e.middleware = append(e.middleware, middleware...)
}

// Handle registers a route with any HTTP method
func (e *Engine) Handle(method, path string, handler HandlerFunc) {
	// Apply middleware in reverse order
	for i := len(e.middleware) - 1; i >= 0; i-- {
		handler = e.middleware[i](handler)
	}
	e.router.handle(method, path, handler)
}

// HTTP method shortcuts
func (e *Engine) GET(path string, h HandlerFunc)     { e.Handle("GET", path, h) }
func (e *Engine) POST(path string, h HandlerFunc)    { e.Handle("POST", path, h) }
func (e *Engine) PUT(path string, h HandlerFunc)     { e.Handle("PUT", path, h) }
func (e *Engine) DELETE(path string, h HandlerFunc)  { e.Handle("DELETE", path, h) }
func (e *Engine) PATCH(path string, h HandlerFunc)   { e.Handle("PATCH", path, h) }
func (e *Engine) OPTIONS(path string, h HandlerFunc) { e.Handle("OPTIONS", path, h) }
func (e *Engine) HEAD(path string, h HandlerFunc)    { e.Handle("HEAD", path, h) }

// Group creates a new route group with a shared prefix
func (e *Engine) Group(prefix string) *Group {
	return &Group{engine: e, prefix: prefix}
}

// Listen starts the HTTP server
func (e *Engine) Listen(addr string) error {
	log.Printf("Blaze running on %s", addr)
	return http.ListenAndServe(addr, e)
}

// ServeHTTP implements http.Handler
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.router.ServeHTTP(w, r)
}

// Group represents a route group with a shared prefix and middleware
type Group struct {
	engine     *Engine
	prefix     string
	middleware []MiddlewareFunc
}

// Use adds middleware to this group
func (g *Group) Use(middleware ...MiddlewareFunc) {
	g.middleware = append(g.middleware, middleware...)
}

// Handle registers a route within the group
func (g *Group) Handle(method, path string, handler HandlerFunc) {
	// Apply group middleware first, then engine middleware
	for i := len(g.middleware) - 1; i >= 0; i-- {
		handler = g.middleware[i](handler)
	}
	for i := len(g.engine.middleware) - 1; i >= 0; i-- {
		handler = g.engine.middleware[i](handler)
	}
	g.engine.router.handle(method, g.prefix+path, handler)
}

// HTTP method shortcuts for Group
func (g *Group) GET(path string, h HandlerFunc)     { g.Handle("GET", path, h) }
func (g *Group) POST(path string, h HandlerFunc)    { g.Handle("POST", path, h) }
func (g *Group) PUT(path string, h HandlerFunc)     { g.Handle("PUT", path, h) }
func (g *Group) DELETE(path string, h HandlerFunc)  { g.Handle("DELETE", path, h) }
func (g *Group) PATCH(path string, h HandlerFunc)   { g.Handle("PATCH", path, h) }
func (g *Group) OPTIONS(path string, h HandlerFunc) { g.Handle("OPTIONS", path, h) }
func (g *Group) HEAD(path string, h HandlerFunc)    { g.Handle("HEAD", path, h) }

// Group creates a nested group
func (g *Group) Group(prefix string) *Group {
	return &Group{
		engine:     g.engine,
		prefix:     g.prefix + prefix,
		middleware: append([]MiddlewareFunc{}, g.middleware...),
	}
}
