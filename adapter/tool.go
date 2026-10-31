package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Handler executes a tool. It receives a context that carries the request
// scope, the per-call timeout and request cancellation; it must honor that
// context (pass it to network calls, check it in loops). Go cannot forcibly
// stop a running function, so a handler that ignores the context keeps
// running to completion and delays the batch.
//
// input is always a valid JSON object. Unless Config.DisableSchemaValidation
// is set, it has also been validated against the tool's InputSchema (see
// schema.go for the supported keywords), so the handler only runs on
// conforming arguments. Handlers should still check domain rules and
// authorization that a schema cannot express.
type Handler func(ctx context.Context, input json.RawMessage) (any, error)

// Tool is a callable function exposed to an application's model loop.
type Tool struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema object advertised to providers. It must be
	// a JSON object with "type": "object". Call arguments are validated
	// against it before the handler runs (see Config.DisableSchemaValidation).
	InputSchema any
	Handler     Handler
}

// NewTool creates a Tool. Registration problems (invalid name, nil handler,
// bad schema, duplicates) are reported by NewExecutor.
func NewTool(name, description string, schema any, handler Handler) Tool {
	return Tool{Name: name, Description: description, InputSchema: schema, Handler: handler}
}

// Defaults applied by NewExecutor to zero Config fields.
const (
	DefaultMaxCalls            = 16
	DefaultMaxInputBytes       = 32 << 10
	DefaultMaxResultBytes      = 256 << 10
	DefaultMaxTotalResultBytes = 1 << 20
	DefaultMaxRequestBytes     = 1 << 20
	DefaultCallTimeout         = 30 * time.Second
	DefaultMaxDuration         = 60 * time.Second

	maxCallIDBytes   = 128
	maxErrorMsgBytes = 1024
)

// Config bounds what one batch may do. Zero fields take the defaults above;
// negative values are rejected by NewExecutor.
type Config struct {
	// MaxCalls is the largest accepted batch (default 16).
	MaxCalls int
	// MaxInputBytes bounds each call's input JSON (default 32 KiB).
	MaxInputBytes int
	// MaxResultBytes bounds each call's encoded output (default 256 KiB).
	MaxResultBytes int
	// MaxTotalResultBytes bounds the aggregate encoded output of a batch
	// (default 1 MiB). Calls that would exceed it fail or are skipped.
	MaxTotalResultBytes int
	// MaxRequestBytes bounds the HTTP request body of InvokeHandler
	// (default 1 MiB).
	MaxRequestBytes int64
	// CallTimeout bounds each call through its context (default 30s).
	CallTimeout time.Duration
	// MaxDuration bounds the whole batch (default 60s). Calls not started
	// when it elapses are reported as timed out without being executed.
	MaxDuration time.Duration
	// DisableSchemaValidation turns off runtime validation of call arguments
	// against each tool's InputSchema, and accepts schemas that use keywords
	// the validator does not implement (such as $ref). Handlers then receive
	// any JSON object and must validate it themselves.
	DisableSchemaValidation bool
}

func (c Config) normalize() (Config, error) {
	if c.MaxCalls < 0 || c.MaxInputBytes < 0 || c.MaxResultBytes < 0 || c.MaxTotalResultBytes < 0 ||
		c.MaxRequestBytes < 0 || c.CallTimeout < 0 || c.MaxDuration < 0 {
		return c, errors.New("adapter: config limits must not be negative")
	}
	if c.MaxCalls == 0 {
		c.MaxCalls = DefaultMaxCalls
	}
	if c.MaxInputBytes == 0 {
		c.MaxInputBytes = DefaultMaxInputBytes
	}
	if c.MaxResultBytes == 0 {
		c.MaxResultBytes = DefaultMaxResultBytes
	}
	if c.MaxTotalResultBytes == 0 {
		c.MaxTotalResultBytes = DefaultMaxTotalResultBytes
	}
	if c.MaxRequestBytes == 0 {
		c.MaxRequestBytes = DefaultMaxRequestBytes
	}
	if c.CallTimeout == 0 {
		c.CallTimeout = DefaultCallTimeout
	}
	if c.MaxDuration == 0 {
		c.MaxDuration = DefaultMaxDuration
	}
	return c, nil
}

// Call is one explicit tool invocation requested by the application.
type Call struct {
	// ID correlates the result with the call. Must be unique within a batch.
	ID   string
	Name string
	// Input is a JSON object. Empty input means {}.
	Input json.RawMessage
}

// Error codes reported in Result.Error.
const (
	CodeHandler = "handler_error"
	// CodeInvalidArguments means the input did not match the tool's
	// InputSchema; the handler was not called. The message lists the
	// violations so a model can correct its call.
	CodeInvalidArguments = "invalid_arguments"
	CodeTimeout          = "timeout"
	CodeCanceled         = "canceled"
	CodeResultTooLarge   = "result_too_large"
	CodeResultEncoding   = "result_encoding"
	CodeOutputBudget     = "output_budget_exceeded"
	CodeSkipped          = "skipped"
	CodePanic            = "panic"
)

// Error codes reported by batch validation (BatchError.Code).
const (
	CodeEmptyBatch    = "empty_batch"
	CodeTooManyCalls  = "too_many_calls"
	CodeInvalidID     = "invalid_id"
	CodeDuplicateID   = "duplicate_id"
	CodeUnknownTool   = "unknown_tool"
	CodeInvalidInput  = "invalid_input"
	CodeInputTooLarge = "input_too_large"
)

// CallError describes why a call produced no output.
type CallError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Result is the outcome of one Call. ID and Name always echo the call.
// Exactly one of Output (OK) and Error is set.
type Result struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	OK     bool            `json:"ok"`
	Output json.RawMessage `json:"output,omitempty"`
	Error  *CallError      `json:"error,omitempty"`
}

// BatchError reports why a whole batch was rejected before anything ran.
// Index is the offending call, or -1 for batch-level problems.
type BatchError struct {
	Index   int
	ID      string
	Code    string
	Message string
}

func (e *BatchError) Error() string {
	if e.Index >= 0 {
		return fmt.Sprintf("adapter: invalid batch: call %d: %s", e.Index, e.Message)
	}
	return "adapter: invalid batch: " + e.Message
}

var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Executor runs validated batches of calls against a fixed set of tools.
// It is safe for concurrent use; it holds no per-call state.
//
// Execution is sequential, in call order, and synchronous: handlers run on the
// calling goroutine with a bounded context, so cancellation is cooperative
// and no background goroutine can outlive a request. Blaze performs no
// deduplication: every accepted call executes every time, so retrying a batch
// repeats its side effects. Applications that need exactly-once behavior must
// make tools idempotent, for example with their own idempotency keys.
type Executor struct {
	cfg     Config
	tools   []Tool
	byName  map[string]Tool
	schemas map[string]*schemaNode // nil when schema validation is disabled
}

// NewExecutor validates the configuration and tools and returns an Executor.
// It rejects: no tools, invalid or duplicate names, nil handlers, and input
// schemas that are not JSON objects with "type": "object" (a nil schema
// becomes an empty object schema). Unless Config.DisableSchemaValidation is
// set, each schema is also compiled for runtime validation, and schemas with
// invalid or unsupported keywords are rejected. Tools are copied; later changes to the
// slice or the Tool values do not affect the Executor.
func NewExecutor(cfg Config, tools ...Tool) (*Executor, error) {
	cfg, err := cfg.normalize()
	if err != nil {
		return nil, err
	}
	if len(tools) == 0 {
		return nil, errors.New("adapter: at least one tool is required")
	}

	e := &Executor{cfg: cfg, byName: make(map[string]Tool, len(tools))}
	if !cfg.DisableSchemaValidation {
		e.schemas = make(map[string]*schemaNode, len(tools))
	}
	for i, t := range tools {
		if !toolNamePattern.MatchString(t.Name) {
			return nil, fmt.Errorf("adapter: tool #%d: invalid name %q (want 1-64 of letters, digits, '_' or '-')", i, t.Name)
		}
		if t.Handler == nil {
			return nil, fmt.Errorf("adapter: tool %q: nil handler", t.Name)
		}
		if _, dup := e.byName[t.Name]; dup {
			return nil, fmt.Errorf("adapter: duplicate tool name %q", t.Name)
		}
		schema, err := normalizeSchema(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("adapter: tool %q: %w", t.Name, err)
		}
		if e.schemas != nil {
			compiled, err := compileSchema(schema, "")
			if err != nil {
				return nil, fmt.Errorf("adapter: tool %q: input %w", t.Name, err)
			}
			e.schemas[t.Name] = compiled
		}
		t.InputSchema = schema
		e.tools = append(e.tools, t)
		e.byName[t.Name] = t
	}
	return e, nil
}

func normalizeSchema(schema any) (map[string]any, error) {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}, nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("input schema is not JSON-encodable: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, errors.New("input schema must be a JSON object")
	}
	if obj["type"] != "object" {
		return nil, errors.New(`input schema must declare "type": "object"`)
	}
	return obj, nil
}

// Config returns the effective configuration, with defaults applied.
func (e *Executor) Config() Config { return e.cfg }

// Tools returns the registered tools in registration order, with normalized
// schemas. Treat the schemas as read-only.
func (e *Executor) Tools() []Tool { return append([]Tool(nil), e.tools...) }

// Validate checks a whole batch without executing anything: it must be
// non-empty and within MaxCalls, and every call needs a unique non-empty ID,
// a registered tool name, and an input within MaxInputBytes that is a valid
// JSON object (empty means {}). The first problem is returned as *BatchError.
func (e *Executor) Validate(calls []Call) error {
	if len(calls) == 0 {
		return &BatchError{Index: -1, Code: CodeEmptyBatch, Message: "at least one call is required"}
	}
	if len(calls) > e.cfg.MaxCalls {
		return &BatchError{Index: -1, Code: CodeTooManyCalls,
			Message: fmt.Sprintf("%d calls exceed the limit of %d", len(calls), e.cfg.MaxCalls)}
	}
	seen := make(map[string]struct{}, len(calls))
	for i, c := range calls {
		bad := func(code, format string, args ...any) error {
			return &BatchError{Index: i, ID: c.ID, Code: code, Message: fmt.Sprintf(format, args...)}
		}
		if c.ID == "" {
			return bad(CodeInvalidID, "id is required")
		}
		if len(c.ID) > maxCallIDBytes || !utf8.ValidString(c.ID) {
			return bad(CodeInvalidID, "id must be valid UTF-8 of at most %d bytes", maxCallIDBytes)
		}
		if _, dup := seen[c.ID]; dup {
			return bad(CodeDuplicateID, "duplicate id %q", c.ID)
		}
		seen[c.ID] = struct{}{}
		if _, ok := e.byName[c.Name]; !ok {
			return bad(CodeUnknownTool, "unknown tool %q", c.Name)
		}
		if len(c.Input) > e.cfg.MaxInputBytes {
			return bad(CodeInputTooLarge, "input of %d bytes exceeds the limit of %d", len(c.Input), e.cfg.MaxInputBytes)
		}
		if _, err := objectInput(c.Input); err != nil {
			return bad(CodeInvalidInput, "%v", err)
		}
	}
	return nil
}

// objectInput returns a private copy of input as a valid JSON object.
// Empty or whitespace-only input means {}.
func objectInput(input json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(trimmed) {
		return nil, errors.New("input is not valid JSON")
	}
	if trimmed[0] != '{' {
		return nil, errors.New("input must be a JSON object")
	}
	return append(json.RawMessage(nil), trimmed...), nil
}

// Execute validates the batch and runs every call, returning results in call
// order. A validation failure returns (nil, *BatchError) with nothing
// executed. If ctx ends mid-batch it returns the results so far and ctx's
// error.
func (e *Executor) Execute(ctx context.Context, calls []Call) ([]Result, error) {
	results := make([]Result, 0, len(calls))
	err := e.Run(ctx, calls, func(r Result) error {
		results = append(results, r)
		return nil
	})
	var be *BatchError
	if errors.As(err, &be) {
		return nil, err
	}
	return results, err
}

// Run validates the batch, then executes the calls one by one, passing each
// Result to emit as soon as it is ready and before the next call starts. It
// stops, returning the error, when ctx is done or emit fails. Per-call
// failures are results, not errors.
//
// Once MaxDuration elapses or MaxTotalResultBytes would be exceeded, remaining
// calls are not executed and are emitted as failed results, so every call
// gets exactly one result unless Run returns an error.
func (e *Executor) Run(ctx context.Context, calls []Call, emit func(Result) error) error {
	if emit == nil {
		return errors.New("adapter: emit must not be nil")
	}
	if err := e.Validate(calls); err != nil {
		return err
	}

	batchCtx, cancel := context.WithTimeout(ctx, e.cfg.MaxDuration)
	defer cancel()

	total := 0
	budgetExhausted := false
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			return err
		}

		var res Result
		switch {
		case budgetExhausted:
			res = failure(call, CodeSkipped, "not executed: the batch output budget was exhausted")
		case batchCtx.Err() != nil:
			res = failure(call, CodeTimeout, "not executed: the batch time budget was exhausted")
		default:
			res = e.invoke(batchCtx, call)
			if res.OK {
				if total+len(res.Output) > e.cfg.MaxTotalResultBytes {
					budgetExhausted = true
					res = failure(call, CodeOutputBudget,
						fmt.Sprintf("result would exceed the batch output limit of %d bytes", e.cfg.MaxTotalResultBytes))
				} else {
					total += len(res.Output)
				}
			}
		}
		if err := emit(res); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func (e *Executor) invoke(ctx context.Context, call Call) (res Result) {
	tool := e.byName[call.Name]
	input, err := objectInput(call.Input)
	if err != nil { // unreachable after Validate; kept to never run a handler on bad input
		return failure(call, CodeInvalidInput, err.Error())
	}

	callCtx, cancel := context.WithTimeout(ctx, e.cfg.CallTimeout)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("adapter: tool %q panicked: %v", call.Name, r)
			res = failure(call, CodePanic, "tool panicked")
		}
	}()

	if err := callCtx.Err(); err != nil {
		return contextFailure(call, err)
	}
	if schema := e.schemas[call.Name]; schema != nil {
		if err := schema.validateInput(input); err != nil {
			return failure(call, CodeInvalidArguments, err.Error())
		}
	}
	out, err := tool.Handler(callCtx, input)
	if ctxErr := callCtx.Err(); ctxErr != nil {
		return contextFailure(call, ctxErr)
	}
	if err != nil {
		return failure(call, CodeHandler, err.Error())
	}

	encoded, err := json.Marshal(out)
	if ctxErr := callCtx.Err(); ctxErr != nil {
		return contextFailure(call, ctxErr)
	}
	if err != nil {
		return failure(call, CodeResultEncoding, "tool result is not JSON-encodable")
	}
	if len(encoded) > e.cfg.MaxResultBytes {
		return failure(call, CodeResultTooLarge,
			fmt.Sprintf("tool result of %d bytes exceeds the limit of %d", len(encoded), e.cfg.MaxResultBytes))
	}
	return Result{ID: call.ID, Name: call.Name, OK: true, Output: encoded}
}

func contextFailure(call Call, err error) Result {
	if errors.Is(err, context.DeadlineExceeded) {
		return failure(call, CodeTimeout, "tool did not finish before its deadline")
	}
	return failure(call, CodeCanceled, "tool call was canceled")
}

func failure(call Call, code, message string) Result {
	return Result{ID: call.ID, Name: call.Name, Error: &CallError{Code: code, Message: truncateMessage(message)}}
}

func truncateMessage(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= maxErrorMsgBytes {
		return s
	}
	cut := maxErrorMsgBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
