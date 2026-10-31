package blaze

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultMaxBodyBytes is the request body limit applied by BindJSON.
const DefaultMaxBodyBytes int64 = 1 << 20

// HTTPError is an error with an HTTP status. The default Engine error handler
// renders it as a plain-text response with Message (or the status text).
type HTTPError struct {
	Status  int
	Message string
	Err     error
}

func (e *HTTPError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%d %s: %v", e.Status, e.Message, e.Err)
	}
	return fmt.Sprintf("%d %s", e.Status, e.Message)
}

func (e *HTTPError) Unwrap() error { return e.Err }

// Context wraps the request and response for convenient access
type Context struct {
	ResponseWriter http.ResponseWriter
	Request        *http.Request
	sw             *statusWriter
}

func newContext(w http.ResponseWriter, r *http.Request) *Context {
	sw := &statusWriter{ResponseWriter: w}
	return &Context{ResponseWriter: sw, Request: r, sw: sw}
}

// committed reports whether response headers were already sent.
func (c *Context) committed() bool { return c.sw != nil && c.sw.wrote }

// statusCode returns the status sent so far (200 if nothing was written).
func (c *Context) statusCode() int {
	if c.sw == nil || c.sw.status == 0 {
		return http.StatusOK
	}
	return c.sw.status
}

// Param returns a path parameter by its pattern name, unescaped.
func (c *Context) Param(key string) string {
	return c.Request.PathValue(key)
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

// SetWriteDeadline sets the write deadline of the underlying connection, for
// handlers that legitimately run longer than the server WriteTimeout. It
// returns an error wrapping http.ErrNotSupported when the server cannot.
func (c *Context) SetWriteDeadline(t time.Time) error {
	return http.NewResponseController(c.ResponseWriter).SetWriteDeadline(t)
}

// String sends a plain text response
func (c *Context) String(code int, msg string) error {
	c.SetHeader("Content-Type", "text/plain; charset=utf-8")
	c.ResponseWriter.WriteHeader(code)
	_, err := c.ResponseWriter.Write([]byte(msg))
	return err
}

// JSON sends a JSON response. The value is encoded before anything is
// written, so an unencodable value returns an error with the response still
// uncommitted.
func (c *Context) JSON(code int, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("blaze: encode JSON response: %w", err)
	}
	c.SetHeader("Content-Type", "application/json")
	c.ResponseWriter.WriteHeader(code)
	_, err = c.ResponseWriter.Write(append(body, '\n'))
	return err
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

// BindJSON decodes a single JSON value from the request body, limited to
// DefaultMaxBodyBytes. Failures are *HTTPError (413 or 400).
func (c *Context) BindJSON(v any) error {
	return c.bindJSON(v, DefaultMaxBodyBytes, false)
}

// BindJSONLimit is BindJSON with an explicit body size limit in bytes.
func (c *Context) BindJSONLimit(v any, limit int64) error {
	return c.bindJSON(v, limit, false)
}

// BindJSONStrict is BindJSONLimit that also rejects unknown object fields.
func (c *Context) BindJSONStrict(v any, limit int64) error {
	return c.bindJSON(v, limit, true)
}

func (c *Context) bindJSON(v any, limit int64, strict bool) error {
	if limit <= 0 {
		limit = DefaultMaxBodyBytes
	}
	body := http.MaxBytesReader(c.ResponseWriter, c.Request.Body, limit)
	defer body.Close()

	dec := json.NewDecoder(body)
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return bindError(err)
	}
	// Exactly one JSON value is allowed.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected data after JSON value")
		}
		return bindError(err)
	}
	return nil
}

func bindError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &HTTPError{Status: http.StatusRequestEntityTooLarge, Message: "request body too large", Err: err}
	}
	return &HTTPError{Status: http.StatusBadRequest, Message: "invalid JSON: " + err.Error(), Err: err}
}

// jsonLine encodes v as one compact JSON line.
func jsonLine(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
