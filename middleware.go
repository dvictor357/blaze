package blaze

import (
	"errors"
	"log"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// Logger returns a middleware that logs request info
func Logger() MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			start := time.Now()
			err := next(c)
			status := "OK"
			if err != nil {
				status = err.Error()
			}
			log.Printf("[%s] %s %d - %v - %v", c.Request.Method, c.Request.URL.Path, c.statusCode(), time.Since(start), status)
			return err
		}
	}
}

// Recovery returns a middleware that recovers from panics. The panic becomes
// an error returned to the engine, so it is rendered by the normal error
// handler, or only logged when the response was already started.
func Recovery() MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) (err error) {
			defer func() {
				if r := recover(); r != nil {
					if r == http.ErrAbortHandler {
						panic(r)
					}
					log.Printf("[PANIC] %v\n%s", r, debug.Stack())
					if c.committed() {
						err = errors.New("handler panicked after the response started")
						return
					}
					err = &HTTPError{Status: http.StatusInternalServerError, Message: "Internal Server Error"}
				}
			}()
			return next(c)
		}
	}
}

// CORSConfig defines CORS options
type CORSConfig struct {
	AllowOrigins []string
	AllowMethods []string
	AllowHeaders []string
	// MaxAge is how long browsers may cache a preflight answer (0 = unset).
	MaxAge time.Duration
}

// DefaultCORSConfig provides defaults that allow any origin. Restrict
// AllowOrigins for anything that is not a public API.
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowHeaders: []string{"Origin", "Content-Type", "Accept", "Authorization"},
	}
}

// CORS returns HTTP middleware that handles CORS. Register it with
// Engine.Wrap so preflight requests are answered before routing and method
// dispatch, whether or not an OPTIONS route exists.
//
// Requests from origins that are not allowed pass through untouched, without
// CORS headers. Credentials are never allowed.
func CORS(config ...CORSConfig) HTTPMiddleware {
	cfg := DefaultCORSConfig()
	if len(config) > 0 {
		cfg = config[0]
	}

	wildcard := false
	allowed := make(map[string]bool, len(cfg.AllowOrigins))
	for _, o := range cfg.AllowOrigins {
		if o == "*" {
			wildcard = true
		}
		allowed[o] = true
	}
	methods := strings.Join(cfg.AllowMethods, ", ")
	headers := strings.Join(cfg.AllowHeaders, ", ")
	maxAge := ""
	if cfg.MaxAge > 0 {
		maxAge = strconv.Itoa(int(cfg.MaxAge / time.Second))
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			h := w.Header()
			h.Add("Vary", "Origin")
			if !wildcard && !allowed[origin] {
				next.ServeHTTP(w, r)
				return
			}
			if wildcard {
				h.Set("Access-Control-Allow-Origin", "*")
			} else {
				h.Set("Access-Control-Allow-Origin", origin)
			}

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				h.Set("Access-Control-Allow-Methods", methods)
				h.Set("Access-Control-Allow-Headers", headers)
				if maxAge != "" {
					h.Set("Access-Control-Max-Age", maxAge)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
