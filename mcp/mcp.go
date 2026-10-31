// Package mcp serves the tools of a Blaze executor as a Model Context
// Protocol (MCP) server, over stdio or Streamable HTTP.
//
// It is a separate Go module so that the core blaze module stays free of
// external dependencies; it uses the official MCP Go SDK
// (github.com/modelcontextprotocol/go-sdk) for the protocol.
//
// Every tools/call goes through adapter.Executor.Execute as a batch of one, so
// MCP calls get exactly the guarantees of the other entry points: input size
// limits, schema validation of arguments, per-call timeouts, output limits and
// panic recovery. Web tools keep their SSRF protection, and the memory tool
// still requires a trusted scope, which Options.Context attaches.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/dvictor357/blaze/adapter"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configures the MCP server.
type Options struct {
	// Name and Version identify the server to clients. Name defaults to
	// "blaze" and Version to "dev".
	Name    string
	Version string
	// Instructions is optional guidance sent to clients during initialization.
	Instructions string

	// Context, if set, runs before every tools/call and returns the context
	// the tool runs in. Use it to attach trusted per-caller values derived
	// from a verified identity, such as tool.WithMemoryScope. Over HTTP,
	// req.Extra.TokenInfo carries the identity set by bearer-token middleware
	// (see github.com/modelcontextprotocol/go-sdk/auth). Never derive a scope
	// from the tool arguments. An error rejects the call with a protocol
	// error and nothing runs.
	//
	// Without it, tools run with no extra values, and the memory tool fails
	// with tool.ErrMemoryScopeRequired.
	Context func(ctx context.Context, req *sdk.CallToolRequest) (context.Context, error)

	// Logger receives SDK diagnostics. Nil discards them. Over stdio it must
	// not write to standard output, which carries the protocol.
	Logger *slog.Logger
}

// NewServer returns an MCP server exposing every tool of ex, with its name,
// description and normalized input schema. Serve it with ServeStdio,
// NewHTTPHandler, or any transport of the MCP SDK.
func NewServer(ex *adapter.Executor, opts Options) *sdk.Server {
	if opts.Name == "" {
		opts.Name = "blaze"
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	srv := sdk.NewServer(&sdk.Implementation{Name: opts.Name, Version: opts.Version}, &sdk.ServerOptions{
		Instructions: opts.Instructions,
		Logger:       opts.Logger,
	})
	b := &bridge{ex: ex, hook: opts.Context}
	for _, t := range ex.Tools() {
		srv.AddTool(&sdk.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}, b.call)
	}
	return srv
}

// ServeStdio serves ex over standard input and output until ctx is done or
// the client disconnects. Nothing else may write to standard output while it
// runs; log to standard error instead.
func ServeStdio(ctx context.Context, ex *adapter.Executor, opts Options) error {
	return NewServer(ex, opts).Run(ctx, &sdk.StdioTransport{})
}

// NewHTTPHandler returns a Streamable HTTP handler for ex. Mount it on one
// path (conventionally /mcp) behind your authentication.
//
// The handler is stateless and answers each POST with a single JSON response,
// so no long-lived streams are held open and it works behind ordinary server
// write timeouts; GET and DELETE answer 405. Request bodies are limited to
// the executor's MaxRequestBytes. It rejects requests with a non-localhost
// Host header that arrive on a loopback address (DNS rebinding), and
// cross-origin browser requests (http.CrossOriginProtection).
//
// It does not authenticate anyone: wrap it, for example with
// auth.RequireBearerToken from the MCP SDK, before exposing it beyond
// loopback, and serve it over TLS. For other transport settings, build your
// own handler from NewServer with sdk.NewStreamableHTTPHandler.
func NewHTTPHandler(ex *adapter.Executor, opts Options) http.Handler {
	srv := NewServer(ex, opts)
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, &sdk.StreamableHTTPOptions{
		Stateless:           true,
		JSONResponse:        true,
		Logger:              opts.Logger,
		MaxRequestBodyBytes: ex.Config().MaxRequestBytes,
	})
	return http.NewCrossOriginProtection().Handler(h)
}

type bridge struct {
	ex   *adapter.Executor
	hook func(context.Context, *sdk.CallToolRequest) (context.Context, error)
}

func (b *bridge) call(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	if b.hook != nil {
		var err error
		if ctx, err = b.hook(ctx, req); err != nil {
			return nil, err
		}
	}
	input := req.Params.Arguments
	if bytes.Equal(bytes.TrimSpace(input), []byte("null")) {
		input = nil // an explicit null means no arguments, like an omitted field
	}
	results, err := b.ex.Execute(ctx, []adapter.Call{{ID: "mcp", Name: req.Params.Name, Input: input}})
	if err != nil {
		var be *adapter.BatchError
		if !errors.As(err, &be) || be.Code == adapter.CodeUnknownTool {
			return nil, err // canceled request, or a tool the SDK should already have rejected
		}
		// Malformed or oversized arguments: report them to the model so it can retry.
		return errorResult(&adapter.CallError{Code: be.Code, Message: be.Message}), nil
	}
	r := results[0]
	if !r.OK {
		return errorResult(r.Error), nil
	}
	res := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(r.Output)}}}
	if len(r.Output) > 0 && r.Output[0] == '{' {
		res.StructuredContent = r.Output
	}
	return res, nil
}

// errorResult is a tool execution error, visible to the model, with the same
// {"error":{"code","message"}} body the provider conversions use.
func errorResult(e *adapter.CallError) *sdk.CallToolResult {
	body, err := json.Marshal(struct {
		Error *adapter.CallError `json:"error"`
	}{e})
	if err != nil {
		body = []byte(`{"error":{"code":"internal","message":"could not encode the error"}}`)
	}
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: string(body)}}}
}
