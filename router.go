package blaze

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// register wires a handler into the standard library mux. Group middleware is
// applied first (innermost), then engine middleware (outermost).
func (e *Engine) register(method, pattern string, handler HandlerFunc, groupMiddleware []MiddlewareFunc) {
	if method == "" || strings.ContainsAny(method, " \t\r\n") {
		panic(fmt.Sprintf("blaze: invalid HTTP method %q", method))
	}
	if !strings.HasPrefix(pattern, "/") {
		panic(fmt.Sprintf("blaze: route pattern %q must start with \"/\"", pattern))
	}
	if handler == nil {
		panic(fmt.Sprintf("blaze: nil handler for %s %s", method, pattern))
	}
	for i := len(groupMiddleware) - 1; i >= 0; i-- {
		handler = groupMiddleware[i](handler)
	}
	for i := len(e.middleware) - 1; i >= 0; i-- {
		handler = e.middleware[i](handler)
	}
	// ServeMux panics on duplicate or ambiguous patterns, which surfaces
	// route conflicts at startup instead of silently misrouting.
	e.mux.Handle(method+" "+pattern, e.dispatch(handler))
}

func (e *Engine) dispatch(handler HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := newContext(w, r)
		if err := handler(c); err != nil {
			e.handleError(c, err)
		}
	})
}

func (e *Engine) handleError(c *Context, err error) {
	if c.committed() {
		log.Printf("blaze: %s %s: error after response started: %v", c.Request.Method, c.Request.URL.Path, err)
		return
	}
	if e.ErrorHandler != nil {
		e.ErrorHandler(c, err)
		return
	}
	defaultErrorHandler(c, err)
}

func defaultErrorHandler(c *Context, err error) {
	var he *HTTPError
	if errors.As(err, &he) {
		status := he.Status
		if status < 400 || status > 599 {
			status = http.StatusInternalServerError
		}
		msg := he.Message
		if msg == "" {
			msg = http.StatusText(status)
		}
		_ = c.String(status, msg)
		return
	}
	log.Printf("blaze: %s %s: %v", c.Request.Method, c.Request.URL.Path, err)
	_ = c.String(http.StatusInternalServerError, "Internal Server Error")
}

// statusWriter records whether and how the response was started so error
// handling never appends to a committed response. It keeps Unwrap so
// http.ResponseController (flush, deadlines, hijack) reaches the real writer.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(p)
}

// FlushError lets http.ResponseController flush through this wrapper while
// keeping the committed flag accurate. An unsupported flush commits nothing.
func (w *statusWriter) FlushError() error {
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err == nil && !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
