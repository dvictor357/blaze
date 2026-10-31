package blaze

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// StreamWriteTimeout bounds each individual write of a stream, so a client
// that stops reading cannot hold the handler forever. It also replaces the
// server-wide WriteTimeout for the lifetime of the stream.
const StreamWriteTimeout = 10 * time.Second

// beginStream sets streaming headers and flushes them. If the writer cannot
// flush, nothing is committed and an error is returned, so callers can still
// answer with a normal error response instead of silently buffering.
func (c *Context) beginStream(contentType string) (*http.ResponseController, error) {
	h := c.ResponseWriter.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")

	rc := http.NewResponseController(c.ResponseWriter)
	if err := rc.Flush(); err != nil {
		h.Del("Content-Type")
		h.Del("Cache-Control")
		h.Del("X-Accel-Buffering")
		return nil, fmt.Errorf("blaze: streaming unsupported by response writer: %w", err)
	}
	_ = rc.SetWriteDeadline(time.Now().Add(StreamWriteTimeout))
	return rc, nil
}

// StreamJSON streams values from dataChan as newline-delimited JSON
// (application/x-ndjson), flushing after each value. It returns nil when the
// channel is closed and the request context's error when the client goes
// away, even while idle. Producers must stop on the same context: a
// producer blocked sending to an abandoned channel would leak.
func (c *Context) StreamJSON(dataChan <-chan any) error {
	rc, err := c.beginStream("application/x-ndjson")
	if err != nil {
		return err
	}
	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case v, ok := <-dataChan:
			if !ok {
				return nil
			}
			line, err := jsonLine(v)
			if err != nil {
				return fmt.Errorf("blaze: encode stream value: %w", err)
			}
			_ = rc.SetWriteDeadline(time.Now().Add(StreamWriteTimeout))
			if _, err := c.ResponseWriter.Write(line); err != nil {
				return err
			}
			if err := rc.Flush(); err != nil {
				return err
			}
		}
	}
}

// SSEWriter writes Server-Sent Events from the handler goroutine. There is no
// producer goroutine or channel: the caller emits events as work completes.
type SSEWriter struct {
	c  *Context
	rc *http.ResponseController
}

// SSE starts a text/event-stream response and flushes the headers. It returns
// an error, with nothing committed, if the response writer cannot flush.
func (c *Context) SSE() (*SSEWriter, error) {
	rc, err := c.beginStream("text/event-stream")
	if err != nil {
		return nil, err
	}
	return &SSEWriter{c: c, rc: rc}, nil
}

// Event writes one event ("event:" and "data:" lines plus the blank-line
// terminator) with data encoded as compact JSON, then flushes. It returns an
// error once the request context is done or a write fails; callers must stop
// producing at that point.
func (s *SSEWriter) Event(name string, data any) error {
	if err := s.c.Request.Context().Err(); err != nil {
		return err
	}
	if name == "" || strings.ContainsAny(name, "\r\n") {
		return errors.New("blaze: invalid SSE event name")
	}
	payload, err := jsonLine(data)
	if err != nil {
		return fmt.Errorf("blaze: encode SSE data: %w", err)
	}
	payload = payload[:len(payload)-1] // compact JSON never contains raw newlines
	frame := make([]byte, 0, len(name)+len(payload)+16)
	frame = append(frame, "event: "...)
	frame = append(frame, name...)
	frame = append(frame, "\ndata: "...)
	frame = append(frame, payload...)
	frame = append(frame, "\n\n"...)

	_ = s.rc.SetWriteDeadline(time.Now().Add(StreamWriteTimeout))
	if _, err := s.c.ResponseWriter.Write(frame); err != nil {
		return err
	}
	return s.rc.Flush()
}
