package blaze

import (
	"log"
	"net/http"
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
			log.Printf("[%s] %s %s - %v", c.Request.Method, c.Request.URL.Path, time.Since(start), status)
			return err
		}
	}
}

// Recovery returns a middleware that recovers from panics
func Recovery() MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) (err error) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[PANIC] %v", r)
					http.Error(c.ResponseWriter, "Internal Server Error", http.StatusInternalServerError)
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
}

// DefaultCORSConfig provides sensible defaults
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowHeaders: []string{"Origin", "Content-Type", "Accept", "Authorization"},
	}
}

// CORS returns a middleware that handles CORS
func CORS(config ...CORSConfig) MiddlewareFunc {
	cfg := DefaultCORSConfig()
	if len(config) > 0 {
		cfg = config[0]
	}

	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			origin := c.Request.Header.Get("Origin")
			if origin == "" {
				return next(c)
			}

			// Check if origin is allowed
			allowed := false
			for _, o := range cfg.AllowOrigins {
				if o == "*" || o == origin {
					allowed = true
					break
				}
			}
			if !allowed {
				return next(c)
			}

			c.SetHeader("Access-Control-Allow-Origin", origin)
			c.SetHeader("Access-Control-Allow-Methods", join(cfg.AllowMethods))
			c.SetHeader("Access-Control-Allow-Headers", join(cfg.AllowHeaders))

			// Handle preflight
			if c.Request.Method == "OPTIONS" {
				return c.NoContent()
			}

			return next(c)
		}
	}
}

// join concatenates strings with comma separator
func join(s []string) string {
	if len(s) == 0 {
		return ""
	}
	result := s[0]
	for _, v := range s[1:] {
		result += ", " + v
	}
	return result
}
