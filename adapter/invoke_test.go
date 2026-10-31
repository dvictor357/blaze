package adapter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dvictor357/blaze"
)

func invokeEngine(t *testing.T, cfg Config, tools ...Tool) *blaze.Engine {
	t.Helper()
	ex := mustExecutor(t, cfg, tools...)
	e := blaze.New()
	e.POST("/invoke", InvokeHandler(ex))
	e.GET("/tools", ListToolsHandler(ex))
	return e
}

func postInvoke(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeResults(t *testing.T, rec *httptest.ResponseRecorder) []Result {
	t.Helper()
	var resp InvokeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not an InvokeResponse: %v\n%s", err, rec.Body.String())
	}
	return resp.Results
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) ErrorDetail {
	t.Helper()
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code == "" {
		t.Fatalf("response is not an ErrorBody: %v\n%s", err, rec.Body.String())
	}
	return body.Error
}

func TestInvoke_ExecutesCallsAndCorrelatesResults(t *testing.T) {
	e := invokeEngine(t, Config{}, echoTool())

	rec := postInvoke(e, `{"calls":[
		{"id":"b","name":"echo","input":{"message":"two"}},
		{"id":"a","name":"echo","input":{"message":"one"}}
	]}`)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	results := decodeResults(t, rec)
	if len(results) != 2 || results[0].ID != "b" || results[1].ID != "a" {
		t.Fatalf("results = %+v", results)
	}
	if !results[0].OK || string(results[0].Output) != `{"echoed":"two"}` || string(results[1].Output) != `{"echoed":"one"}` {
		t.Errorf("outputs = %s / %s", results[0].Output, results[1].Output)
	}
}

func TestInvoke_RejectsLegacyAndUnknownPayloadsBeforeAnySideEffect(t *testing.T) {
	bodies := map[string]string{
		"chat messages only":    `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`,
		"messages with calls":   `{"calls":[{"id":"1","name":"count","input":{}}],"messages":[{"role":"assistant","tool_calls":[]}]}`,
		"unknown call field":    `{"calls":[{"id":"1","name":"count","input":{},"extra":true}]}`,
		"unknown top-level key": `{"calls":[{"id":"1","name":"count"}],"scope":"someone-else"}`,
		"malformed":             `{"calls":[`,
		"trailing data":         `{"calls":[{"id":"1","name":"count"}]} {"calls":[]}`,
		"not an object":         `[{"id":"1","name":"count"}]`,
		"wrong id type":         `{"calls":[{"id":1,"name":"count"}]}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			n := 0
			e := invokeEngine(t, Config{}, counterTool("count", &n))
			rec := postInvoke(e, body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400\n%s", rec.Code, rec.Body.String())
			}
			if d := decodeError(t, rec); d.Code != "invalid_request" {
				t.Errorf("code = %q", d.Code)
			}
			if n != 0 {
				t.Errorf("a tool ran %d time(s) for a rejected request", n)
			}
		})
	}
}

func TestInvoke_InvalidBatchesAreRejectedWholesale(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		body string
		code string
		idx  int // expected call_index, -1 when batch-level
	}{
		{"empty calls", Config{}, `{"calls":[]}`, CodeEmptyBatch, -1},
		{"missing calls", Config{}, `{}`, CodeEmptyBatch, -1},
		{"too many", Config{MaxCalls: 2}, `{"calls":[{"id":"1","name":"count"},{"id":"2","name":"count"},{"id":"3","name":"count"}]}`, CodeTooManyCalls, -1},
		{"empty id", Config{}, `{"calls":[{"id":"1","name":"count"},{"id":"","name":"count"}]}`, CodeInvalidID, 1},
		{"duplicate id", Config{}, `{"calls":[{"id":"1","name":"count"},{"id":"1","name":"count"}]}`, CodeDuplicateID, 1},
		{"unknown tool", Config{}, `{"calls":[{"id":"1","name":"count"},{"id":"2","name":"ghost"}]}`, CodeUnknownTool, 1},
		{"array input", Config{}, `{"calls":[{"id":"1","name":"count"},{"id":"2","name":"count","input":[1]}]}`, CodeInvalidInput, 1},
		{"null input", Config{}, `{"calls":[{"id":"1","name":"count","input":null}]}`, CodeInvalidInput, 0},
		{"oversized input", Config{MaxInputBytes: 20}, `{"calls":[{"id":"1","name":"count","input":{"k":"` + strings.Repeat("v", 50) + `"}}]}`, CodeInputTooLarge, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := 0
			e := invokeEngine(t, tt.cfg, counterTool("count", &n))
			rec := postInvoke(e, tt.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422\n%s", rec.Code, rec.Body.String())
			}
			d := decodeError(t, rec)
			if d.Code != tt.code {
				t.Errorf("code = %q, want %q", d.Code, tt.code)
			}
			if tt.idx < 0 && d.CallIndex != nil {
				t.Errorf("batch-level error carries call_index %d", *d.CallIndex)
			}
			if tt.idx >= 0 && (d.CallIndex == nil || *d.CallIndex != tt.idx) {
				t.Errorf("call_index = %v, want %d", d.CallIndex, tt.idx)
			}
			if n != 0 {
				t.Errorf("valid members ran %d time(s) before the batch was rejected", n)
			}
		})
	}
}

func TestInvoke_ContentTypeAndBodyLimits(t *testing.T) {
	n := 0
	e := invokeEngine(t, Config{MaxRequestBytes: 256}, counterTool("count", &n))
	body := `{"calls":[{"id":"1","name":"count"}]}`

	send := func(contentType, payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(payload))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	if rec := send("application/json; charset=utf-8", body); rec.Code != 200 {
		t.Errorf("json with charset = %d", rec.Code)
	}
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "application/jsonp"} {
		if rec := send(ct, body); rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q = %d, want 415", ct, rec.Code)
		}
	}
	before := n
	huge := `{"calls":[{"id":"1","name":"count","input":{"k":"` + strings.Repeat("v", 1000) + `"}}]}`
	rec := send("application/json", huge)
	if rec.Code != http.StatusRequestEntityTooLarge || decodeError(t, rec).Code != "request_too_large" {
		t.Errorf("oversized body = %d %s", rec.Code, rec.Body.String())
	}
	if n != before {
		t.Error("an oversized request must not execute anything")
	}
}

func TestInvoke_FailuresAreCorrelatedResults(t *testing.T) {
	failing := NewTool("fail", "fails", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		return nil, errors.New("said \"no\"\nand left")
	})
	unencodable := NewTool("bad", "returns garbage", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		return make(chan int), nil
	})
	e := invokeEngine(t, Config{}, failing, unencodable, echoTool())

	rec := postInvoke(e, `{"calls":[
		{"id":"f","name":"fail"},{"id":"g","name":"bad"},{"id":"h","name":"echo","input":{"message":"still runs"}}
	]}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (failures are per-call)\n%s", rec.Code, rec.Body.String())
	}
	results := decodeResults(t, rec) // valid JSON despite quotes and newlines in the error
	if r := results[0]; r.ID != "f" || r.OK || r.Error.Code != CodeHandler || r.Error.Message != "said \"no\"\nand left" {
		t.Errorf("handler failure = %+v", r)
	}
	if r := results[1]; r.ID != "g" || r.OK || r.Error.Code != CodeResultEncoding {
		t.Errorf("serialization failure = %+v", r)
	}
	if r := results[2]; r.ID != "h" || !r.OK {
		t.Errorf("later call = %+v", r)
	}
}

func TestInvoke_ScopeComesFromServerMiddlewareNotTheBody(t *testing.T) {
	type scopeKey struct{}
	who := NewTool("who", "returns the trusted scope", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		return ctx.Value(scopeKey{}), nil
	})
	ex := mustExecutor(t, Config{}, who)

	e := blaze.New()
	auth := e.Group("")
	auth.Use(func(next blaze.HandlerFunc) blaze.HandlerFunc {
		return func(c *blaze.Context) error {
			// Stand-in for real authentication: derive the scope server-side.
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), scopeKey{}, "tenant-1"))
			return next(c)
		}
	})
	auth.POST("/invoke", InvokeHandler(ex))

	rec := postInvoke(e, `{"calls":[{"id":"1","name":"who"}]}`)
	if got := string(decodeResults(t, rec)[0].Output); got != `"tenant-1"` {
		t.Errorf("tool saw scope %s", got)
	}

	// A caller cannot smuggle a scope in through the payload.
	for _, body := range []string{
		`{"calls":[{"id":"1","name":"who"}],"scope":"victim"}`,
		`{"calls":[{"id":"1","name":"who","input":{"scope":"victim"},"scope":"victim"}]}`,
	} {
		if rec := postInvoke(e, body); rec.Code != http.StatusBadRequest {
			t.Errorf("payload %s = %d, want 400", body, rec.Code)
		}
	}
}

func readSSEFrame(t *testing.T, r *bufio.Reader) (event, data string) {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("reading stream: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			return event, data
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		default:
			t.Fatalf("unexpected SSE line %q", line)
		}
	}
}

func TestInvokeStream_SendsEachResultBeforeTheNextCallFinishes(t *testing.T) {
	release := make(chan struct{})
	quick := NewTool("quick", "returns at once", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		return "first", nil
	})
	gated := NewTool("gated", "waits for the test", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		select {
		case <-release:
			return "second", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	e := invokeEngine(t, Config{}, quick, gated)
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Post(srv.URL+"/invoke", "application/json",
		strings.NewReader(`{"stream":true,"calls":[{"id":"1","name":"quick"},{"id":"2","name":"gated"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("response = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	r := bufio.NewReader(resp.Body)
	// "gated" is still blocked: this frame proves results are sent as they complete.
	ev, data := readSSEFrame(t, r)
	var first Result
	if err := json.Unmarshal([]byte(data), &first); err != nil || ev != EventResult || first.ID != "1" || string(first.Output) != `"first"` {
		t.Fatalf("first frame = %q %q (%v)", ev, data, err)
	}

	close(release)
	ev, data = readSSEFrame(t, r)
	var second Result
	if err := json.Unmarshal([]byte(data), &second); err != nil || ev != EventResult || second.ID != "2" || string(second.Output) != `"second"` {
		t.Fatalf("second frame = %q %q (%v)", ev, data, err)
	}
	if ev, data = readSSEFrame(t, r); ev != EventDone || data != `{"count":2}` {
		t.Errorf("final frame = %q %q", ev, data)
	}
	if _, err := r.ReadString('\n'); err == nil {
		t.Error("stream should end after the done event")
	}
}

func TestInvokeStream_InvalidBatchIsAJSONErrorNotAStream(t *testing.T) {
	n := 0
	e := invokeEngine(t, Config{}, counterTool("count", &n))

	rec := postInvoke(e, `{"stream":true,"calls":[{"id":"1","name":"count"},{"id":"1","name":"count"}]}`)
	if rec.Code != http.StatusUnprocessableEntity || strings.Contains(rec.Header().Get("Content-Type"), "event-stream") {
		t.Errorf("response = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if n != 0 {
		t.Error("nothing may run for an invalid batch")
	}
}

func TestInvokeStream_ClientDisconnectCancelsRemainingWork(t *testing.T) {
	var lastRan bool
	slowCanceled := make(chan struct{})
	quick := NewTool("quick", "returns at once", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		return "first", nil
	})
	slow := NewTool("slow", "waits until canceled", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		<-ctx.Done()
		close(slowCanceled)
		return nil, ctx.Err()
	})
	last := NewTool("last", "must never run", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		lastRan = true
		return "late", nil
	})
	ex := mustExecutor(t, Config{}, quick, slow, last)

	handlerDone := make(chan struct{})
	invoke := InvokeHandler(ex)
	e := blaze.New()
	e.POST("/invoke", func(c *blaze.Context) error {
		defer close(handlerDone)
		return invoke(c)
	})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/invoke",
		strings.NewReader(`{"stream":true,"calls":[{"id":"1","name":"quick"},{"id":"2","name":"slow"},{"id":"3","name":"last"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	readSSEFrame(t, bufio.NewReader(resp.Body)) // first result arrived; "slow" is running

	cancel()
	for name, ch := range map[string]chan struct{}{"the running tool's context": slowCanceled, "the handler": handlerDone} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s to wind down after the client left", name)
		}
	}
	if lastRan {
		t.Error("a call after the disconnect was executed")
	}
}

func TestInvoke_ClientDisconnectCancelsToolContext(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	waiting := NewTool("wait", "waits until canceled", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	e := invokeEngine(t, Config{}, waiting)
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/invoke", strings.NewReader(`{"calls":[{"id":"1","name":"wait"}]}`))
	req.Header.Set("Content-Type", "application/json")
	go func() {
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("tool context was not canceled when the client disconnected")
	}
}

func TestListToolsHandler(t *testing.T) {
	e := invokeEngine(t, Config{}, echoTool(), NewTool("other", "Another tool", map[string]any{"type": "object"},
		func(ctx context.Context, in json.RawMessage) (any, error) { return nil, nil }))

	rec := do(e, "/tools")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		OpenAI []struct {
			Type     string `json:"type"`
			Function struct {
				Name       string         `json:"name"`
				Parameters map[string]any `json:"parameters"`
			} `json:"function"`
		} `json:"openai"`
		Anthropic []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"input_schema"`
		} `json:"anthropic"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 2 || len(resp.OpenAI) != 2 || len(resp.Anthropic) != 2 {
		t.Fatalf("counts = %d/%d/%d", resp.Count, len(resp.OpenAI), len(resp.Anthropic))
	}
	if resp.OpenAI[0].Type != "function" || resp.OpenAI[0].Function.Name != "echo" || resp.OpenAI[0].Function.Parameters["type"] != "object" {
		t.Errorf("openai[0] = %+v", resp.OpenAI[0])
	}
	if resp.Anthropic[1].Name != "other" || resp.Anthropic[1].InputSchema["type"] != "object" {
		t.Errorf("anthropic[1] = %+v", resp.Anthropic[1])
	}
}

func do(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}
