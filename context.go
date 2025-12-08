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
