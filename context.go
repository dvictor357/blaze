package blaze

import (
	"encoding/json"
	"net/http"
)

// Context wraps the request and response for convenient access
type Context struct {
	ResponseWriter http.ResponseWriter
	Request        *http.Request
	params         map[string]string
	statusCode     int
}

// Param returns a URL path parameter by key
func (c *Context) Param(key string) string {
	return c.params[key]
}

// Query returns a query parameter by key
func (c *Context) Query(key string) string {
	return c.Request.URL.Query().Get(key)
}

// QueryDefault returns a query parameter or a default value if not set
func (c *Context) QueryDefault(key, defaultVal string) string {
	if val := c.Query(key); val != "" {
		return val
	}
	return defaultVal
}

// SetHeader sets a response header
func (c *Context) SetHeader(key, value string) {
	c.ResponseWriter.Header().Set(key, value)
}

// Status sets the HTTP status code (chainable)
func (c *Context) Status(code int) *Context {
	c.statusCode = code
	return c
}

// String sends a plain text response
func (c *Context) String(code int, msg string) error {
	c.SetHeader("Content-Type", "text/plain; charset=utf-8")
	c.ResponseWriter.WriteHeader(code)
	_, err := c.ResponseWriter.Write([]byte(msg))
	return err
}

// JSON sends a JSON response
func (c *Context) JSON(code int, data any) error {
	c.SetHeader("Content-Type", "application/json")
	c.ResponseWriter.WriteHeader(code)
	return json.NewEncoder(c.ResponseWriter).Encode(data)
}

// HTML sends an HTML response
func (c *Context) HTML(code int, html string) error {
	c.SetHeader("Content-Type", "text/html; charset=utf-8")
	c.ResponseWriter.WriteHeader(code)
	_, err := c.ResponseWriter.Write([]byte(html))
	return err
}

// Redirect sends an HTTP redirect
func (c *Context) Redirect(code int, url string) error {
	http.Redirect(c.ResponseWriter, c.Request, url, code)
	return nil
}

// NoContent sends a 204 No Content response
func (c *Context) NoContent() error {
	c.ResponseWriter.WriteHeader(http.StatusNoContent)
	return nil
}

// BindJSON decodes the request body as JSON
func (c *Context) BindJSON(v any) error {
	defer c.Request.Body.Close()
	return json.NewDecoder(c.Request.Body).Decode(v)
}

// StreamJSON streams JSON objects from a channel
func (c *Context) StreamJSON(dataChan <-chan any) error {
	c.SetHeader("Content-Type", "application/json")
	c.SetHeader("Transfer-Encoding", "chunked")

	encoder := json.NewEncoder(c.ResponseWriter)
	for data := range dataChan {
		if err := encoder.Encode(data); err != nil {
			return err
		}
		if f, ok := c.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}
	return nil
}
