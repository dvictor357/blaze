package blaze

import (
	"log"
	"net/http"
)

type HandlerFunc func(*Context) error

type Engine struct {
	router     *Router
	middleware []func(HandlerFunc) HandlerFunc
}

func New() *Engine {
	return &Engine{
		router: newRouter(),
	}
}

func (e *Engine) Use(middleware ...func(HandlerFunc) HandlerFunc) {
	e.middleware = append(e.middleware, middleware...)
}

func (e *Engine) GET(path string, handler HandlerFunc) {
	e.router.handle("GET", path, handler)
}

func (e *Engine) POST(path string, handler HandlerFunc) {
	e.router.handle("POST", path, handler)
}

func (e *Engine) PUT(path string, handler HandlerFunc) {
	e.router.handle("PUT", path, handler)
}

func (e *Engine) DELETE(path string, handler HandlerFunc) {
	e.router.handle("DELETE", path, handler)
}

func (e *Engine) OPTIONS(path string, handler HandlerFunc) {
	e.router.handle("OPTIONS", path, handler)
}

func (e *Engine) HEAD(path string, handler HandlerFunc) {
	e.router.handle("HEAD", path, handler)
}

func (e *Engine) PATCH(path string, handler HandlerFunc) {
	e.router.handle("PATCH", path, handler)
}

func (e *Engine) TRACE(path string, handler HandlerFunc) {
	e.router.handle("TRACE", path, handler)
}

func (e *Engine) CONNECT(path string, handler HandlerFunc) {
	e.router.handle("CONNECT", path, handler)
}

func (e *Engine) handle(method, path string, handler HandlerFunc) {
	// Wrap handler with middlewares
	for i := len(e.middleware) - 1; i >= 0; i-- {
		handler = e.middleware[i](handler)
	}
	e.router.handle(method, path, handler)
}

func (e *Engine) Listen(addr string) error {
	log.Printf("Blaze running on %s", addr)
	return http.ListenAndServe(addr, e)
}

func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.router.ServeHTTP(w, r)
}
