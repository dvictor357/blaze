package blaze

import (
	"encoding/json"
	"net/http"
)

type Context struct {
	ResponseWriter http.ResponseWriter
	Request        *http.Request
	params         map[string]string
}

func (c *Context) Param(key string) string {
	return c.params[key]
}

func (c *Context) String(code int, msg string) error {
	c.ResponseWriter.Header().Set("Content-Type", "text/plain")
	c.ResponseWriter.WriteHeader(code)
	_, err := c.ResponseWriter.Write([]byte(msg))
	return err
}

func (c *Context) JSON(code int, data any) error {
	c.ResponseWriter.Header().Set("Content-Type", "application/json")
	c.ResponseWriter.WriteHeader(code)
	return json.NewEncoder(c.ResponseWriter).Encode(data)
}

func (c *Context) BindJSON(v any) error {
	defer c.Request.Body.Close()
	return json.NewDecoder(c.Request.Body).Decode(v)
}

func (c *Context) StreamJSONStream(dataChan <-chan map[string]any) error {
	c.ResponseWriter.Header().Set("Content-Type", "application/json")
	c.ResponseWriter.Header().Set("Transfer-Encoding", "chunked")

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
