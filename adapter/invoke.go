package adapter

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/dvictor357/blaze"
)

// ============================================================================
// Invocation wire contract
// ============================================================================

// CallRequest is one requested call in an InvokeRequest.
type CallRequest struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// InvokeRequest is the only accepted body of InvokeHandler. Unknown fields
// (for example a chat "messages" array) are rejected before anything runs.
type InvokeRequest struct {
	Calls []CallRequest `json:"calls"`
	// Stream selects a text/event-stream response instead of one JSON body.
	Stream bool `json:"stream,omitempty"`
}

// InvokeResponse is the JSON response body of a non-streaming invocation.
type InvokeResponse struct {
	Results []Result `json:"results"`
}

// ErrorDetail describes a rejected request.
type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	CallIndex *int   `json:"call_index,omitempty"`
	CallID    string `json:"call_id,omitempty"`
}

// ErrorBody is the JSON body of every non-2xx InvokeHandler response.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// Event names of the streaming response.
const (
	// EventResult carries one Result as soon as its call finished.
	EventResult = "result"
	// EventDone is the last event of a stream that ran to completion; its
	// data is {"count": <number of results sent>}.
	EventDone = "done"
)

// writeSlack extends the write deadline past MaxDuration so the final
// response can still be written.
const writeSlack = 10 * time.Second

// InvokeHandler returns the handler of the single invocation endpoint,
// intended for POST. The body is
//
//	{"calls":[{"id":"c1","name":"tool","input":{...}}], "stream": false}
//
// The whole batch is validated before any call runs; invalid batches are
// rejected with a JSON ErrorBody (400 malformed or unknown fields, 413 body
// too large, 415 wrong content type, 422 batch validation).
//
// With "stream": false (default) it answers 200 with an InvokeResponse. With
// "stream": true it answers a text/event-stream whose "result" events are Blaze
// execution events, one per call, flushed before the next call starts,
// followed by a "done" event. This is not a provider (OpenAI/Anthropic)
// stream. If the client disconnects, the request context is canceled and no
// further call starts.
//
// Authentication and trusted per-caller scoping (such as tool.WithMemoryScope)
// are the application's job: perform them in middleware that runs before this
// handler. Blaze executes every accepted call; it does not deduplicate retries.
func InvokeHandler(ex *Executor) blaze.HandlerFunc {
	cfg := ex.Config()
	return func(c *blaze.Context) error {
		if !isJSONContentType(c.Request.Header.Get("Content-Type")) {
			return writeInvokeError(c, http.StatusUnsupportedMediaType, ErrorDetail{
				Code: "unsupported_media_type", Message: "Content-Type must be application/json",
			})
		}

		var req InvokeRequest
		if err := c.BindJSONStrict(&req, cfg.MaxRequestBytes); err != nil {
			var he *blaze.HTTPError
			if errors.As(err, &he) && he.Status == http.StatusRequestEntityTooLarge {
				return writeInvokeError(c, http.StatusRequestEntityTooLarge, ErrorDetail{
					Code: "request_too_large", Message: he.Message,
				})
			}
			msg := "invalid request body"
			if he != nil {
				msg = he.Message
			}
			return writeInvokeError(c, http.StatusBadRequest, ErrorDetail{Code: "invalid_request", Message: msg})
		}

		calls := make([]Call, len(req.Calls))
		for i, cr := range req.Calls {
			calls[i] = Call{ID: cr.ID, Name: cr.Name, Input: cr.Input}
		}
		if err := ex.Validate(calls); err != nil {
			var be *BatchError
			if errors.As(err, &be) {
				detail := ErrorDetail{Code: be.Code, Message: be.Message, CallID: be.ID}
				if be.Index >= 0 {
					idx := be.Index
					detail.CallIndex = &idx
				}
				return writeInvokeError(c, http.StatusUnprocessableEntity, detail)
			}
			return err
		}

		// A batch may legitimately outlast the server-wide WriteTimeout.
		_ = c.SetWriteDeadline(time.Now().Add(cfg.MaxDuration + writeSlack))

		ctx := c.Request.Context()
		if req.Stream {
			return invokeStream(c, ex, calls)
		}
		results, err := ex.Execute(ctx, calls)
		if err != nil {
			return err // client went away or the engine is shutting down
		}
		return c.JSON(http.StatusOK, InvokeResponse{Results: results})
	}
}

func invokeStream(c *blaze.Context, ex *Executor, calls []Call) error {
	sse, err := c.SSE()
	if err != nil {
		return writeInvokeError(c, http.StatusInternalServerError, ErrorDetail{
			Code: "streaming_unsupported", Message: "this server connection cannot stream",
		})
	}
	sent := 0
	err = ex.Run(c.Request.Context(), calls, func(r Result) error {
		if err := sse.Event(EventResult, r); err != nil {
			return err
		}
		sent++
		return nil
	})
	if err != nil {
		return err // failed write or canceled request: stop, nothing more to say
	}
	return sse.Event(EventDone, map[string]int{"count": sent})
}

func writeInvokeError(c *blaze.Context, status int, detail ErrorDetail) error {
	return c.JSON(status, ErrorBody{Error: detail})
}

func isJSONContentType(header string) bool {
	if strings.TrimSpace(header) == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(header)
	return err == nil && mt == "application/json"
}

// ============================================================================
// Tool discovery
// ============================================================================

// ToolListResponse is the response of ListToolsHandler: the same registered
// tools as provider tool definitions, for applications to pass to their model.
type ToolListResponse struct {
	OpenAI    []OpenAIToolDef    `json:"openai"`
	Anthropic []AnthropicToolDef `json:"anthropic"`
	Count     int                `json:"count"`
}

// ListToolsHandler returns a handler that lists the executor's tools as
// OpenAI and Anthropic tool definitions. It reveals tool names and schemas, so
// protect it like the invocation endpoint.
func ListToolsHandler(ex *Executor) blaze.HandlerFunc {
	tools := ex.Tools()
	return func(c *blaze.Context) error {
		resp := ToolListResponse{
			OpenAI:    make([]OpenAIToolDef, len(tools)),
			Anthropic: make([]AnthropicToolDef, len(tools)),
			Count:     len(tools),
		}
		for i, t := range tools {
			resp.OpenAI[i] = t.ToOpenAI()
			resp.Anthropic[i] = t.ToAnthropic()
		}
		return c.JSON(http.StatusOK, resp)
	}
}
