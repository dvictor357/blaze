package blaze

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func streamClient() *http.Client { return &http.Client{Timeout: 15 * time.Second} }

// readFrame reads lines up to the blank line that terminates an SSE frame.
func readFrame(t *testing.T, r *bufio.Reader) []string {
	t.Helper()
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("reading stream after %q: %v", lines, err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return lines
		}
		lines = append(lines, line)
	}
}

func sameLines(got, want []string) bool {
	return strings.Join(got, "\n") == strings.Join(want, "\n")
}

func TestSSE_EventsAreFlushedAsTheyHappen(t *testing.T) {
	release := make(chan struct{})
	e := New()
	e.Use(Logger()) // wrapping the writer must not break flushing
	e.GET("/s", func(c *Context) error {
		sse, err := c.SSE()
		if err != nil {
			return err
		}
		if err := sse.Event("tick", map[string]int{"n": 1}); err != nil {
			return err
		}
		select {
		case <-release:
		case <-c.Request.Context().Done():
			return nil
		}
		return sse.Event("tick", map[string]int{"n": 2})
	})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	resp, err := streamClient().Get(srv.URL + "/s")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", resp.Header.Get("Cache-Control"))
	}

	r := bufio.NewReader(resp.Body)
	// The handler is still blocked, so receiving this frame proves it was flushed.
	if got := readFrame(t, r); !sameLines(got, []string{"event: tick", `data: {"n":1}`}) {
		t.Errorf("first frame = %q", got)
	}
	close(release)
	if got := readFrame(t, r); !sameLines(got, []string{"event: tick", `data: {"n":2}`}) {
		t.Errorf("second frame = %q", got)
	}
}

func TestSSE_ProducerStopsWhenClientDisconnects(t *testing.T) {
	stopped := make(chan error, 1)
	e := New()
	e.GET("/s", func(c *Context) error {
		sse, err := c.SSE()
		if err != nil {
			return err
		}
		for i := 0; ; i++ {
			if err := sse.Event("tick", i); err != nil {
				stopped <- err
				return nil
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/s", nil)
	resp, err := streamClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	readFrame(t, bufio.NewReader(resp.Body))

	cancel()
	select {
	case err := <-stopped:
		if err == nil {
			t.Error("Event returned nil after the client left; it must report the failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("producer kept running after the client disconnected")
	}
}

// plainWriter is a ResponseWriter that cannot flush.
type plainWriter struct {
	h    http.Header
	code int
	body bytes.Buffer
}

func (p *plainWriter) Header() http.Header         { return p.h }
func (p *plainWriter) Write(b []byte) (int, error) { return p.body.Write(b) }
func (p *plainWriter) WriteHeader(code int)        { p.code = code }

func TestSSE_UnflushableWriterFailsCleanly(t *testing.T) {
	e := New()
	var sseErr error
	e.GET("/s", func(c *Context) error {
		_, sseErr = c.SSE()
		if sseErr != nil {
			return c.String(http.StatusInternalServerError, "cannot stream")
		}
		return nil
	})

	w := &plainWriter{h: http.Header{}}
	e.ServeHTTP(w, httptest.NewRequest("GET", "/s", nil))

	if sseErr == nil {
		t.Fatal("SSE on a writer that cannot flush must return an error")
	}
	if w.code != http.StatusInternalServerError || w.body.String() != "cannot stream" {
		t.Errorf("fallback response = %d %q", w.code, w.body.String())
	}
	if ct := w.h.Get("Content-Type"); strings.Contains(ct, "event-stream") {
		t.Errorf("streaming headers leaked into the fallback response: %q", ct)
	}
}

func TestSSE_EventValidation(t *testing.T) {
	e := New()
	var errs []error
	e.GET("/s", func(c *Context) error {
		sse, err := c.SSE()
		if err != nil {
			return err
		}
		errs = append(errs,
			sse.Event("", 1),
			sse.Event("bad\nname", 1),
			sse.Event("bad\rname", 1),
			sse.Event("ok", make(chan int)), // not JSON-encodable
		)
		return sse.Event("ok", map[string]string{"line": "a\nb"})
	})

	rec := do(e, "GET", "/s")
	for i, err := range errs {
		if err == nil {
			t.Errorf("invalid event %d was accepted", i)
		}
	}
	// Only the valid event produced output, and embedded newlines are escaped
	// so they cannot break the frame.
	want := "event: ok\ndata: {\"line\":\"a\\nb\"}\n\n"
	if rec.Body.String() != want {
		t.Errorf("body = %q, want %q", rec.Body.String(), want)
	}
}

func TestStreamJSON_WritesNDJSONAndFlushes(t *testing.T) {
	e := New()
	e.GET("/n", func(c *Context) error {
		ch := make(chan any)
		go func() {
			defer close(ch)
			for i := 1; i <= 2; i++ {
				select {
				case ch <- map[string]int{"n": i}:
				case <-c.Request.Context().Done():
					return
				}
			}
		}()
		return c.StreamJSON(ch)
	})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	resp, err := streamClient().Get(srv.URL + "/n")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q", ct)
	}
	r := bufio.NewReader(resp.Body)
	for i := 1; i <= 2; i++ {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if want := `{"n":` + string(rune('0'+i)) + "}\n"; line != want {
			t.Errorf("line %d = %q, want %q", i, line, want)
		}
	}
	if _, err := r.ReadString('\n'); err == nil {
		t.Error("stream should end when the channel closes")
	}
}

func TestStreamJSON_ReturnsWhenClientLeavesWhileIdle(t *testing.T) {
	returned := make(chan error, 1)
	e := New()
	e.GET("/idle", func(c *Context) error {
		never := make(chan any) // no producer ever sends
		returned <- c.StreamJSON(never)
		return nil
	})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/idle", nil)
	resp, err := streamClient().Do(req) // headers are flushed immediately
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	cancel()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("StreamJSON returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StreamJSON never returned for a disconnected idle client")
	}
}

func TestStreamJSON_UnencodableValueEndsStream(t *testing.T) {
	e := New()
	e.GET("/n", func(c *Context) error {
		ch := make(chan any, 1)
		ch <- make(chan int)
		return c.StreamJSON(ch)
	})
	rec := do(e, "GET", "/n")
	if strings.Contains(rec.Body.String(), "Internal Server Error") {
		t.Errorf("error text must not be appended to a started stream: %q", rec.Body.String())
	}
}
