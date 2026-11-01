package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dvictor357/blaze"
	"github.com/dvictor357/blaze/adapter"
	blazemcp "github.com/dvictor357/blaze/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed index.html
var dashboardHTML string

type sourceKey struct{}

func requestSource(ctx context.Context) string {
	if source, _ := ctx.Value(sourceKey{}).(string); source == "MCP" {
		return source
	}
	return "HTTP"
}

func newExecutor(s *store) (*adapter.Executor, error) {
	t := adapter.NewTool("reserve_inventory", "Reserve Mechanical Keyboard stock for an order. Identical order IDs and arguments return the existing reservation; conflicting reuse or insufficient stock is rejected without changing inventory.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sku":      map[string]any{"type": "string", "enum": []string{"KB-001"}},
			"quantity": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
			"order_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
		},
		"required":             []string{"sku", "quantity", "order_id"},
		"additionalProperties": false,
	}, func(ctx context.Context, input json.RawMessage) (any, error) {
		var in reserveInput
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, err
		}
		out, err := s.reserve(ctx, in, requestSource(ctx))
		if err != nil {
			code, message := "inventory_error", "Inventory operation failed; no reservation was committed"
			var domain *domainError
			if errors.As(err, &domain) {
				code, message = domain.Code, domain.Message
			} else {
				log.Printf("inventory reservation: %v", err)
			}
			recordRejection(s, ctx, requestSource(ctx), code, message, in)
			return nil, &domainError{code, message}
		}
		return out, nil
	})
	return adapter.NewExecutor(adapter.Config{MaxRequestBytes: 64 << 10, MaxResultBytes: 4 << 10, MaxTotalResultBytes: 64 << 10}, t)
}

func recordRejection(s *store, ctx context.Context, source, code, message string, in reserveInput) {
	// Audit rejected requests even when their caller has just disconnected.
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := s.recordRejection(auditCtx, source, code, message, in); err != nil {
		log.Printf("inventory audit: %v", err)
	}
}

// boundedCapture keeps only the adapter's bounded request/response sizes. It
// never buffers a response instead of delivering it to the real client.
type boundedCapture struct {
	bytes.Buffer
	limit int
}

func (b *boundedCapture) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := b.limit - b.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}

type auditWriter struct {
	http.ResponseWriter
	body   boundedCapture
	status int
}

func (w *auditWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *auditWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_, _ = w.body.Write(p)
	return w.ResponseWriter.Write(p)
}

// auditInvoke reuses the actual adapter wire contract. Only failures before a
// handler runs are logged here; the inventory handler owns business outcomes.
func auditInvoke(s *store, ex *adapter.Executor, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody := boundedCapture{limit: int(ex.Config().MaxRequestBytes)}
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.TeeReader(r.Body, &requestBody), r.Body}
		response := &auditWriter{ResponseWriter: w, body: boundedCapture{limit: 128 << 10}}
		next.ServeHTTP(response, r)
		var request adapter.InvokeRequest
		_ = json.Unmarshal(requestBody.Bytes(), &request)
		inputFor := func(id string) reserveInput {
			for _, call := range request.Calls {
				if call.ID == id {
					var in reserveInput
					_ = json.Unmarshal(call.Input, &in)
					return in
				}
			}
			return reserveInput{}
		}
		if response.status >= 400 {
			var body adapter.ErrorBody
			if json.Unmarshal(response.body.Bytes(), &body) != nil || body.Error.Code == "" {
				body.Error = adapter.ErrorDetail{Code: "request_rejected", Message: "Invocation request was rejected before execution"}
			}
			recordRejection(s, r.Context(), "HTTP", body.Error.Code, body.Error.Message, inputFor(body.Error.CallID))
			return
		}
		auditResult := func(result adapter.Result) {
			if result.Error != nil && result.Error.Code == adapter.CodeInvalidArguments {
				recordRejection(s, r.Context(), "HTTP", result.Error.Code, result.Error.Message, inputFor(result.ID))
			}
		}
		if strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
			for _, line := range strings.Split(response.body.String(), "\n") {
				if strings.HasPrefix(line, "data: ") {
					var result adapter.Result
					if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &result) == nil {
						auditResult(result)
					}
				}
			}
			return
		}
		var body adapter.InvokeResponse
		if json.Unmarshal(response.body.Bytes(), &body) == nil {
			for _, result := range body.Results {
				auditResult(result)
			}
		}
	})
}

func requireToken(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte("Bearer " + token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		have := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(have[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="inventory-demo"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Bearer token required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}

func newHandler(s *store, token, addr string) (http.Handler, error) {
	tmpl, err := template.New("dashboard").Parse(dashboardHTML)
	if err != nil {
		return nil, err
	}
	ex, err := newExecutor(s)
	if err != nil {
		return nil, err
	}
	invoke := blaze.New()
	invoke.POST("/api/invoke", adapter.InvokeHandler(ex))
	mcp := blazemcp.NewHTTPHandler(ex, blazemcp.Options{
		Name: "blaze-inventory", Version: "0.1.0",
		Instructions: "Local demo inventory. Use reserve_inventory only when the user explicitly requests a reservation.",
		Context: func(ctx context.Context, _ *sdk.CallToolRequest) (context.Context, error) {
			return context.WithValue(ctx, sourceKey{}, "MCP"), nil
		},
	})
	api := http.NewServeMux()
	api.Handle("POST /api/invoke", auditInvoke(s, ex, invoke))
	api.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		state, err := s.state(r.Context())
		if err != nil {
			log.Printf("read inventory: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Could not read inventory"})
			return
		}
		writeJSON(w, http.StatusOK, state)
	})
	api.Handle("POST /api/claude", newClaudeHandler(token, addr))
	mux := http.NewServeMux()
	mux.Handle("/api/", requireToken(token, api))
	mux.Handle("/mcp", requireToken(token, mcp))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, struct{ Token string }{token}); err != nil {
			log.Printf("render dashboard: %v", err)
		}
	})
	protected := http.NewCrossOriginProtection().Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		if !loopbackHost(r.Host) {
			http.Error(w, "Loopback Host required", http.StatusForbidden)
			return
		}
		protected.ServeHTTP(w, r)
	}), nil
}

func run(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("inventory-demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:18084", "loopback HTTP listen address")
	dbPath := fs.String("db", "inventory.db", "SQLite inventory database path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || !loopbackHost(host) {
		return fmt.Errorf("-addr must be a loopback host:port, got %q", *addr)
	}
	token := os.Getenv("BLAZE_DEMO_TOKEN")
	if token == "" {
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			return err
		}
		token = hex.EncodeToString(random[:])
		fmt.Fprintf(stderr, "BLAZE_DEMO_TOKEN not set; generated token for this run: %s\n", token)
	}
	s, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer s.db.Close()
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	handler, err := newHandler(s, token, listener.Addr().String())
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 120 * time.Second,
		IdleTimeout: 120 * time.Second, MaxHeaderBytes: 32 << 10,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	fmt.Fprintf(stderr, "Inventory demo: http://%s (SQLite: %s)\n", listener.Addr(), *dbPath)
	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		log.Fatal(err)
	}
}
