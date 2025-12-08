package blaze

import (
	"log"
	"net/http"
	"time"
)

func Logger() func(HandlerFunc) HandlerFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			start := time.Now()
			err := next(c)
			log.Printf("%s %s %s %s", c.Request.Method, c.Request.URL.Path, time.Since(start), err)
			return err
		}
	}
}

func Recovery() func(HandlerFunc) HandlerFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			defer func() {
				if err := recover(); err != nil {
					c.ResponseWriter.WriteHeader(http.StatusInternalServerError)
					c.ResponseWriter.Write([]byte("Internal Server Error"))
				}
			}()
			return next(c)
		}
	}
}
