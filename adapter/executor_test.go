package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func call(id, name, input string) Call {
	return Call{ID: id, Name: name, Input: json.RawMessage(input)}
}

func mustExecutor(t *testing.T, cfg Config, tools ...Tool) *Executor {
	t.Helper()
	ex, err := NewExecutor(cfg, tools...)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	return ex
}

func echoTool() Tool {
	return NewTool("echo", "Echo back the message", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{"type": "string"},
		},
	}, func(ctx context.Context, input json.RawMessage) (any, error) {
		var data struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(input, &data); err != nil {
			return nil, err
		}
		return map[string]string{"echoed": data.Message}, nil
	})
}

// counterTool counts how many times it really executed.
func counterTool(name string, n *int) Tool {
	return NewTool(name, "counts executions", nil, func(ctx context.Context, input json.RawMessage) (any, error) {
		*n++
		return map[string]int{"n": *n}, nil
	})
}

func TestNewExecutor_RejectsBadRegistrations(t *testing.T) {
	ok := func(ctx context.Context, in json.RawMessage) (any, error) { return nil, nil }
	tests := []struct {
		name  string
		cfg   Config
		tools []Tool
	}{
		{"no tools", Config{}, nil},
		{"empty name", Config{}, []Tool{NewTool("", "d", nil, ok)}},
		{"name with space", Config{}, []Tool{NewTool("my tool", "d", nil, ok)}},
		{"name too long", Config{}, []Tool{NewTool(strings.Repeat("a", 65), "d", nil, ok)}},
		{"nil handler", Config{}, []Tool{NewTool("t", "d", nil, nil)}},
		{"duplicate names", Config{}, []Tool{NewTool("t", "d", nil, ok), NewTool("t", "e", nil, ok)}},
		{"schema is an array", Config{}, []Tool{NewTool("t", "d", []string{"x"}, ok)}},
		{"schema is a string", Config{}, []Tool{NewTool("t", "d", "object", ok)}},
		{"schema without type object", Config{}, []Tool{NewTool("t", "d", map[string]any{"type": "string"}, ok)}},
		{"schema not encodable", Config{}, []Tool{NewTool("t", "d", map[string]any{"type": "object", "x": make(chan int)}, ok)}},
		{"negative limit", Config{MaxCalls: -1}, []Tool{NewTool("t", "d", nil, ok)}},
		{"negative timeout", Config{CallTimeout: -time.Second}, []Tool{NewTool("t", "d", nil, ok)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ex, err := NewExecutor(tt.cfg, tt.tools...); err == nil {
				t.Errorf("NewExecutor accepted it: %+v", ex)
			}
		})
	}
}

func TestNewExecutor_NormalizesAndCopies(t *testing.T) {
	n := 0
	tools := []Tool{counterTool("count", &n)}
	ex := mustExecutor(t, Config{}, tools...)

	// A nil schema becomes an empty object schema for providers.
	got := ex.Tools()[0].ToOpenAI().Function.Parameters
	raw, _ := json.Marshal(got)
	if string(raw) != `{"properties":{},"type":"object"}` {
		t.Errorf("normalized schema = %s", raw)
	}

	// Later changes to the caller's slice or the returned copy do not leak in.
	tools[0].Name = "renamed"
	ex.Tools()[0].Name = "mutated"
	if _, err := ex.Execute(context.Background(), []Call{call("1", "count", "")}); err != nil {
		t.Errorf("executor affected by caller mutation: %v", err)
	}

	cfg := ex.Config()
	if cfg.MaxCalls != DefaultMaxCalls || cfg.CallTimeout != DefaultCallTimeout || cfg.MaxResultBytes != DefaultMaxResultBytes {
		t.Errorf("defaults not applied: %+v", cfg)
	}
}

func TestExecute_InvalidBatchRunsNothing(t *testing.T) {
	big := `{"x":"` + strings.Repeat("a", 200) + `"}`
	tests := []struct {
		name  string
		calls []Call
		cfg   Config
		code  string
	}{
		{"empty batch", nil, Config{}, CodeEmptyBatch},
		{"too many calls", []Call{call("1", "count", ""), call("2", "count", ""), call("3", "count", "")}, Config{MaxCalls: 2}, CodeTooManyCalls},
		{"missing id", []Call{call("1", "count", ""), call("", "count", "")}, Config{}, CodeInvalidID},
		{"overlong id", []Call{call(strings.Repeat("i", 129), "count", "")}, Config{}, CodeInvalidID},
		{"duplicate id", []Call{call("1", "count", ""), call("1", "count", "")}, Config{}, CodeDuplicateID},
		{"unknown tool last", []Call{call("1", "count", ""), call("2", "missing", "")}, Config{}, CodeUnknownTool},
		{"input not JSON", []Call{call("1", "count", ""), call("2", "count", "{nope")}, Config{}, CodeInvalidInput},
		{"input is an array", []Call{call("1", "count", ""), call("2", "count", "[1]")}, Config{}, CodeInvalidInput},
		{"input is a string", []Call{call("1", "count", `"hi"`)}, Config{}, CodeInvalidInput},
		{"input is null", []Call{call("1", "count", "null")}, Config{}, CodeInvalidInput},
		{"input has trailing data", []Call{call("1", "count", `{} {}`)}, Config{}, CodeInvalidInput},
		{"input too large", []Call{call("1", "count", ""), call("2", "count", big)}, Config{MaxInputBytes: 100}, CodeInputTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := 0
			ex := mustExecutor(t, tt.cfg, counterTool("count", &n))

			results, err := ex.Execute(context.Background(), tt.calls)
			var be *BatchError
			if !errors.As(err, &be) {
				t.Fatalf("error = %v, want *BatchError", err)
			}
			if be.Code != tt.code {
				t.Errorf("code = %q, want %q (%v)", be.Code, tt.code, be)
			}
			if results != nil {
				t.Errorf("results = %v, want none", results)
			}
			if n != 0 {
				t.Errorf("a valid member ran %d time(s) although the batch was invalid", n)
			}
		})
	}
}

func TestBatchError_PointsAtTheOffendingCall(t *testing.T) {
	n := 0
	ex := mustExecutor(t, Config{}, counterTool("count", &n))
	err := ex.Validate([]Call{call("a", "count", ""), call("b", "nope", "")})
	var be *BatchError
	if !errors.As(err, &be) || be.Index != 1 || be.ID != "b" {
		t.Errorf("BatchError = %+v, want index 1 id b", be)
	}
}

func TestExecute_CorrelatesResultsInOrder(t *testing.T) {
	ex := mustExecutor(t, Config{}, echoTool())

	results, err := ex.Execute(context.Background(), []Call{
		call("z-last-id", "echo", `{"message":"one"}`),
		call("a-first-id", "echo", `{"message":"two"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results", len(results))
	}
	wantIDs := []string{"z-last-id", "a-first-id"}
	wantOut := []string{`{"echoed":"one"}`, `{"echoed":"two"}`}
	for i, r := range results {
		if r.ID != wantIDs[i] || r.Name != "echo" || !r.OK || r.Error != nil || string(r.Output) != wantOut[i] {
			t.Errorf("result %d = %+v", i, r)
		}
	}
}

func TestExecute_HandlerReceivesObjectInput(t *testing.T) {
	var seen []string
	tool := NewTool("see", "records input", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		seen = append(seen, string(in))
		if len(in) > 0 {
			in[0] = 'X' // a handler scribbling on its input must not corrupt the caller's data
		}
		return "ok", nil
	})
	ex := mustExecutor(t, Config{}, tool)

	callerInput := json.RawMessage(`  {"a": 1}  `)
	if _, err := ex.Execute(context.Background(), []Call{
		{ID: "1", Name: "see"},                     // omitted input
		{ID: "2", Name: "see", Input: callerInput}, // object input
	}); err != nil {
		t.Fatal(err)
	}
	if seen[0] != `{}` || seen[1] != `{"a": 1}` {
		t.Errorf("handler inputs = %q", seen)
	}
	if string(callerInput) != `  {"a": 1}  ` {
		t.Errorf("caller's input was modified: %q", callerInput)
	}
}

func TestExecute_HandlerErrorIsAResultNotAFailure(t *testing.T) {
	n := 0
	failing := NewTool("fail", "always fails", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		return nil, errors.New(`bad "input"` + "\nsecond line")
	})
	ex := mustExecutor(t, Config{}, failing, counterTool("count", &n))

	results, err := ex.Execute(context.Background(), []Call{call("1", "fail", ""), call("2", "count", "")})
	if err != nil {
		t.Fatal(err)
	}
	if r := results[0]; r.OK || r.Error == nil || r.Error.Code != CodeHandler || r.Error.Message != "bad \"input\"\nsecond line" || r.Output != nil {
		t.Errorf("failing result = %+v", r)
	}
	if !results[1].OK || n != 1 {
		t.Errorf("a failing call must not stop the batch: %+v (ran %d)", results[1], n)
	}
}

func TestExecute_SerializationFailureIsReported(t *testing.T) {
	n := 0
	bad := NewTool("bad", "returns an unencodable value", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		return map[string]any{"ch": make(chan int)}, nil
	})
	ex := mustExecutor(t, Config{}, bad, counterTool("count", &n))

	results, err := ex.Execute(context.Background(), []Call{call("1", "bad", ""), call("2", "count", "")})
	if err != nil {
		t.Fatal(err)
	}
	if r := results[0]; r.OK || r.Error == nil || r.Error.Code != CodeResultEncoding {
		t.Errorf("unencodable result = %+v", r)
	}
	if !results[1].OK {
		t.Errorf("later call should still run: %+v", results[1])
	}
}

func TestExecute_ResultSizeLimit(t *testing.T) {
	big := NewTool("big", "large output", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		return strings.Repeat("x", 500), nil
	})
	ex := mustExecutor(t, Config{MaxResultBytes: 100}, big, echoTool())

	results, _ := ex.Execute(context.Background(), []Call{call("1", "big", ""), call("2", "echo", `{"message":"small"}`)})
	if r := results[0]; r.OK || r.Error == nil || r.Error.Code != CodeResultTooLarge || len(r.Output) != 0 {
		t.Errorf("oversized result = %+v", r)
	}
	if !results[1].OK {
		t.Errorf("a small result after an oversized one must still succeed: %+v", results[1])
	}
}

func TestExecute_AggregateOutputBudget(t *testing.T) {
	ran := 0
	chunk := NewTool("chunk", "100 byte output", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		ran++
		return strings.Repeat("x", 98), nil // encodes to 100 bytes
	})
	ex := mustExecutor(t, Config{MaxTotalResultBytes: 250}, chunk)

	results, err := ex.Execute(context.Background(), []Call{
		call("1", "chunk", ""), call("2", "chunk", ""), call("3", "chunk", ""), call("4", "chunk", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("every call must get a result, got %d", len(results))
	}
	if !results[0].OK || !results[1].OK {
		t.Errorf("first two fit the budget: %+v %+v", results[0], results[1])
	}
	if results[2].OK || results[2].Error.Code != CodeOutputBudget {
		t.Errorf("third result = %+v, want %s", results[2], CodeOutputBudget)
	}
	if results[3].OK || results[3].Error.Code != CodeSkipped {
		t.Errorf("fourth result = %+v, want %s", results[3], CodeSkipped)
	}
	if ran != 3 {
		t.Errorf("handlers ran %d times, want 3 (the skipped call must not run)", ran)
	}
}

func TestExecute_CallTimeoutIsCooperative(t *testing.T) {
	n := 0
	slow := NewTool("slow", "waits for its context", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ex := mustExecutor(t, Config{CallTimeout: 30 * time.Millisecond}, slow, counterTool("count", &n))

	start := time.Now()
	results, err := ex.Execute(context.Background(), []Call{call("1", "slow", ""), call("2", "count", "")})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("timeout was not applied")
	}
	if r := results[0]; r.OK || r.Error.Code != CodeTimeout {
		t.Errorf("timed-out result = %+v", r)
	}
	if !results[1].OK {
		t.Errorf("one slow call must not poison the next: %+v", results[1])
	}
}

func TestExecute_BatchDurationBudget(t *testing.T) {
	n := 0
	slow := NewTool("slow", "waits for its context", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ex := mustExecutor(t, Config{MaxDuration: 30 * time.Millisecond}, slow, counterTool("count", &n))

	results, err := ex.Execute(context.Background(), []Call{call("1", "slow", ""), call("2", "count", "")})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].OK || results[0].Error.Code != CodeTimeout {
		t.Errorf("first result = %+v", results[0])
	}
	if results[1].OK || results[1].Error.Code != CodeTimeout || n != 0 {
		t.Errorf("call after the batch deadline must not run: %+v (ran %d)", results[1], n)
	}
}

func TestExecute_PanicIsContained(t *testing.T) {
	n := 0
	boom := NewTool("boom", "panics", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		panic("secret internal state")
	})
	ex := mustExecutor(t, Config{}, boom, counterTool("count", &n))

	results, err := ex.Execute(context.Background(), []Call{call("1", "boom", ""), call("2", "count", "")})
	if err != nil {
		t.Fatal(err)
	}
	if r := results[0]; r.OK || r.Error.Code != CodePanic || strings.Contains(r.Error.Message, "secret") {
		t.Errorf("panic result = %+v", r)
	}
	if !results[1].OK {
		t.Errorf("the server and later calls must survive a panic: %+v", results[1])
	}
}

func TestExecute_ParentCancellationStopsTheBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n := 0
	first := NewTool("first", "cancels the request", nil, func(c context.Context, in json.RawMessage) (any, error) {
		cancel()
		return "done", nil
	})
	ex := mustExecutor(t, Config{}, first, counterTool("count", &n))

	results, err := ex.Execute(ctx, []Call{call("1", "first", ""), call("2", "count", "")})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(results) != 1 || n != 0 {
		t.Errorf("got %d results and %d later executions; the canceled batch must stop", len(results), n)
	}
}

func TestExecute_HandlerSeesRequestContextValues(t *testing.T) {
	type key struct{}
	tool := NewTool("who", "reads scope", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
		return ctx.Value(key{}), nil
	})
	ex := mustExecutor(t, Config{}, tool)

	ctx := context.WithValue(context.Background(), key{}, "tenant-7")
	results, _ := ex.Execute(ctx, []Call{call("1", "who", "")})
	if string(results[0].Output) != `"tenant-7"` {
		t.Errorf("handler context lost request values: %+v", results[0])
	}
}

func TestRun_EmitsEachResultBeforeTheNextCallStarts(t *testing.T) {
	var trace []string
	mk := func(name string) Tool {
		return NewTool(name, "traces", nil, func(ctx context.Context, in json.RawMessage) (any, error) {
			trace = append(trace, "run:"+name)
			return name, nil
		})
	}
	ex := mustExecutor(t, Config{}, mk("a"), mk("b"))

	err := ex.Run(context.Background(), []Call{call("1", "a", ""), call("2", "b", "")}, func(r Result) error {
		trace = append(trace, "emit:"+r.ID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(trace, " "); got != "run:a emit:1 run:b emit:2" {
		t.Errorf("order = %s", got)
	}
}

func TestRun_StopsWhenEmitFails(t *testing.T) {
	n := 0
	ex := mustExecutor(t, Config{}, counterTool("count", &n))
	errGone := errors.New("client gone")

	err := ex.Run(context.Background(), []Call{call("1", "count", ""), call("2", "count", ""), call("3", "count", "")},
		func(r Result) error { return errGone })
	if !errors.Is(err, errGone) {
		t.Errorf("err = %v", err)
	}
	if n != 1 {
		t.Errorf("ran %d calls after the first emit failed, want 1 total", n)
	}
}

func TestRun_RequiresEmit(t *testing.T) {
	n := 0
	ex := mustExecutor(t, Config{}, counterTool("count", &n))
	if err := ex.Run(context.Background(), []Call{call("1", "count", "")}, nil); err == nil || n != 0 {
		t.Errorf("nil emit: err=%v, ran %d", err, n)
	}
}

func TestExecutor_IsSafeForConcurrentUse(t *testing.T) {
	ex := mustExecutor(t, Config{}, echoTool())

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msg := fmt.Sprintf("m%d", i)
			results, err := ex.Execute(context.Background(), []Call{call("1", "echo", `{"message":"`+msg+`"}`)})
			if err != nil || string(results[0].Output) != `{"echoed":"`+msg+`"}` {
				t.Errorf("worker %d: %v %+v", i, err, results)
			}
		}()
	}
	wg.Wait()
}

type deadlineResult struct {
	ctx context.Context
}

func (r deadlineResult) MarshalJSON() ([]byte, error) {
	<-r.ctx.Done()
	return []byte(`true`), nil
}

func TestExecute_ExpiredCallCannotReportSuccess(t *testing.T) {
	for _, stage := range []string{"handler", "encoding"} {
		t.Run(stage, func(t *testing.T) {
			late := NewTool("late", "returns after its deadline", nil, func(ctx context.Context, _ json.RawMessage) (any, error) {
				if stage == "encoding" {
					return deadlineResult{ctx: ctx}, nil
				}
				<-ctx.Done()
				return true, nil
			})
			ex := mustExecutor(t, Config{CallTimeout: time.Millisecond}, late)
			results, err := ex.Execute(context.Background(), []Call{call("1", "late", "")})
			if err != nil {
				t.Fatal(err)
			}
			r := results[0]
			if r.OK || r.Error == nil || r.Error.Code != CodeTimeout || len(r.Output) != 0 {
				t.Fatalf("expired %s result = %+v", stage, r)
			}
		})
	}
}

func TestExecute_LastCallCancellationIsReturned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := NewTool("stop", "cancels its request", nil, func(context.Context, json.RawMessage) (any, error) {
		cancel()
		return true, nil
	})
	ex := mustExecutor(t, Config{}, stop)
	results, err := ex.Execute(ctx, []Call{call("1", "stop", "")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute error = %v, want context.Canceled", err)
	}
	if len(results) != 1 || results[0].OK || results[0].Error == nil || results[0].Error.Code != CodeCanceled {
		t.Fatalf("canceled last call = %+v", results)
	}
}
