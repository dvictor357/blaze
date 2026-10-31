package blaze

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServer_DefaultsAreBounded(t *testing.T) {
	e := New()
	srv := e.Server("127.0.0.1:0")

	if srv.Handler != http.Handler(e) {
		t.Error("server must serve the engine")
	}
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Errorf("all timeouts must be set: header=%v read=%v write=%v idle=%v",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes <= 0 {
		t.Error("MaxHeaderBytes must be bounded")
	}
}

func TestServer_DropsClientsThatNeverFinishTheirHeaders(t *testing.T) {
	e := New()
	e.GET("/", func(c *Context) error { return c.String(200, "ok") })

	srv := e.Server("")
	srv.ReadHeaderTimeout = 100 * time.Millisecond

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// A request line and a header, but never the terminating blank line.
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: example\r\n")); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadAll(conn); err != nil {
		t.Errorf("server did not close the stalled connection: %v", err)
	}
}
