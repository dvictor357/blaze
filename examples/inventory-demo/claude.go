package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// This is a local demo bridge to the installed Claude Code CLI, not a model
// client provided by Blaze. Only the inventory MCP tool is enabled.
func newClaudeHandler(token, addr string) http.HandlerFunc {
	busy := make(chan struct{}, 1)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fail := func(status int, message string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
		}
		var input struct {
			Prompt string `json:"prompt"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&input); err != nil {
			fail(http.StatusBadRequest, "expected a JSON object with prompt")
			return
		}
		if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
			fail(http.StatusBadRequest, "expected exactly one JSON object")
			return
		}
		if len(strings.TrimSpace(input.Prompt)) == 0 || len(input.Prompt) > 2000 {
			fail(http.StatusBadRequest, "prompt must contain 1–2000 bytes")
			return
		}
		select {
		case busy <- struct{}{}:
			defer func() { <-busy }()
		default:
			fail(http.StatusConflict, "a Claude Code request is already running")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
		defer cancel()
		config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
			"inventory": map[string]any{
				"type": "http", "url": "http://" + addr + "/mcp",
				"headers": map[string]string{"Authorization": "Bearer " + token},
			},
		}})
		if err != nil {
			fail(http.StatusInternalServerError, "cannot configure local MCP connection")
			return
		}
		cmd := exec.CommandContext(ctx, "claude", "-p", input.Prompt,
			"--model", "sonnet", "--effort", "low",
			"--tools", "", "--allowedTools", "mcp__inventory__reserve_inventory",
			"--permission-mode", "dontAsk", "--strict-mcp-config", "--mcp-config", string(config),
			"--setting-sources", "", "--settings", `{"disableAllHooks":true}`,
			"--no-chrome", "--no-session-persistence", "--disable-slash-commands",
			"--output-format", "stream-json", "--verbose",
			"--system-prompt", "You are the inventory assistant in a local Blaze demonstration. Use the inventory MCP tool for requested reservations. Do not invent tool results. Call the requested reservation only once. Reply in concise English with the actual reservation ID and remaining stock. You have no file, shell, or web tools.")
		// An existing Claude Code session may set nesting markers. This isolated
		// subprocess is a separate, explicitly requested demo session.
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CLAUDECODE=") && !strings.HasPrefix(entry, "CLAUDE_CODE_ENTRYPOINT=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		started := time.Now()
		err = cmd.Run()
		if ctx.Err() != nil {
			fail(http.StatusGatewayTimeout, "Claude Code request timed out or was cancelled; check inventory before retrying")
			return
		}
		if err != nil {
			// Do not expose arbitrary diagnostics or the MCP configuration token.
			fail(http.StatusBadGateway, "Claude Code failed; check local CLI authentication and model access before retrying")
			fmt.Fprintln(os.Stderr, "inventory demo: Claude Code exited unsuccessfully")
			return
		}
		type toolCall struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		calls := make([]toolCall, 0)
		reply := ""
		stream := json.NewDecoder(&stdout)
		for {
			var event struct {
				Type    string `json:"type"`
				Result  string `json:"result"`
				IsError bool   `json:"is_error"`
				Message struct {
					Content []struct {
						Type  string          `json:"type"`
						Name  string          `json:"name"`
						Input json.RawMessage `json:"input"`
					} `json:"content"`
				} `json:"message"`
			}
			if err := stream.Decode(&event); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				fail(http.StatusBadGateway, "Claude Code returned an unreadable event stream; check stock before retrying")
				return
			}
			if event.Type == "assistant" {
				for _, block := range event.Message.Content {
					if block.Type == "tool_use" {
						calls = append(calls, toolCall{Name: block.Name, Input: block.Input})
					}
				}
			}
			if event.Type == "result" {
				if event.IsError {
					fail(http.StatusBadGateway, "Claude Code reported an error; check inventory before retrying")
					return
				}
				reply = event.Result
			}
		}
		if reply == "" {
			fail(http.StatusBadGateway, "Claude Code did not return a final response; check stock before retrying")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"prompt": input.Prompt, "reply": reply, "tool_calls": calls,
			"duration_ms": time.Since(started).Milliseconds(),
		})
	}
}
