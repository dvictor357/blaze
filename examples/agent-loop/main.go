// Command agent-loop runs a complete model round trip with Blaze's built-in
// tools, against OpenAI and against Anthropic.
//
// For each provider it sends the tool definitions, executes the tool calls the
// model makes through the Blaze executor, returns the results, and repeats
// until the model answers in text. It uses only the standard library: Blaze's
// adapter types are the providers' wire shapes, so no SDK is needed (the
// official SDKs work just as well; see integration/providers).
//
//	OPENAI_API_KEY=...    go run ./examples/agent-loop -provider openai
//	ANTHROPIC_API_KEY=... go run ./examples/agent-loop -provider anthropic
//	go run ./examples/agent-loop            # both; skips a provider without a key
//
// OPENAI_MODEL and ANTHROPIC_MODEL override the default models. Each run makes
// a few billed API requests.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dvictor357/blaze/adapter"
	"github.com/dvictor357/blaze/tool"
)

const (
	defaultOpenAIModel    = "gpt-5-mini"
	defaultAnthropicModel = "claude-opus-5-5"
	maxTurns              = 8
	maxErrorBody          = 2 << 10
)

const prompt = `Use the tools to answer, then reply in two short sentences.
1. What is the current date and time in Tokyo?
2. In this JSON, what is the email of the user whose role is "admin"?
   {"users":[{"name":"Ana","role":"dev","email":"ana@example.com"},{"name":"Bo","role":"admin","email":"bo@example.com"}]}
3. Save that email in memory under the key "admin_email".`

var (
	httpClient = &http.Client{Timeout: 120 * time.Second}
	// Endpoints are variables only so the offline test can point them at a fake.
	openAIURL    = "https://api.openai.com/v1/chat/completions"
	anthropicURL = "https://api.anthropic.com/v1/messages"
)

func newExecutor() (*adapter.Executor, error) {
	return adapter.NewExecutor(adapter.Config{},
		tool.NewDateTimeTool(),
		tool.NewJSONQueryTool(),
		tool.NewMemoryTool(tool.NewMemoryStore(tool.MemoryOptions{})),
	)
}

// postJSON sends body and decodes a 2xx response into out. Error bodies are
// truncated; the API key is only ever placed in a header.
func postJSON(ctx context.Context, url string, header http.Header, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header = header
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return fmt.Errorf("%s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

func logResults(results []adapter.Result) {
	for _, r := range results {
		if r.OK {
			log.Printf("  tool %s -> %s", r.Name, r.Output)
		} else {
			log.Printf("  tool %s -> error %s: %s", r.Name, r.Error.Code, r.Error.Message)
		}
	}
}

// ---------------------------------------------------------------------------
// OpenAI Chat Completions
// ---------------------------------------------------------------------------

func runOpenAI(ctx context.Context, ex *adapter.Executor, key, model string) (string, error) {
	tools := make([]adapter.OpenAIToolDef, 0, len(ex.Tools()))
	for _, t := range ex.Tools() {
		tools = append(tools, t.ToOpenAI())
	}
	// Messages are kept as raw JSON so the assistant messages are sent back
	// exactly as the model produced them.
	user, _ := json.Marshal(map[string]string{"role": "user", "content": prompt})
	messages := []json.RawMessage{user}

	header := http.Header{"Authorization": {"Bearer " + key}}
	for turn := 0; turn < maxTurns; turn++ {
		var resp struct {
			Choices []struct {
				FinishReason string          `json:"finish_reason"`
				Message      json.RawMessage `json:"message"`
			} `json:"choices"`
		}
		err := postJSON(ctx, openAIURL, header, map[string]any{
			"model": model, "messages": messages, "tools": tools,
		}, &resp)
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", errors.New("OpenAI returned no choices")
		}
		raw := resp.Choices[0].Message
		var msg struct {
			Content   *string                  `json:"content"`
			ToolCalls []adapter.OpenAIToolCall `json:"tool_calls"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			return "", err
		}
		messages = append(messages, raw)
		if len(msg.ToolCalls) == 0 {
			if msg.Content == nil {
				return "", fmt.Errorf("no text and no tool calls (finish_reason %q)", resp.Choices[0].FinishReason)
			}
			return *msg.Content, nil
		}

		// The model's current tool calls -> Blaze calls -> tool messages.
		calls, err := adapter.OpenAICalls(msg.ToolCalls)
		if err != nil {
			return "", err
		}
		results, err := ex.Execute(ctx, calls)
		if err != nil {
			return "", err
		}
		logResults(results)
		toolMsgs, err := adapter.OpenAIToolMessages(results)
		if err != nil {
			return "", err
		}
		for _, m := range toolMsgs {
			b, _ := json.Marshal(m)
			messages = append(messages, b)
		}
	}
	return "", fmt.Errorf("no final answer after %d turns", maxTurns)
}

// ---------------------------------------------------------------------------
// Anthropic Messages
// ---------------------------------------------------------------------------

func runAnthropic(ctx context.Context, ex *adapter.Executor, key, model string) (string, error) {
	tools := make([]adapter.AnthropicToolDef, 0, len(ex.Tools()))
	for _, t := range ex.Tools() {
		tools = append(tools, t.ToAnthropic())
	}
	type message struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	messages := []message{{Role: "user", Content: prompt}}

	header := http.Header{
		"X-Api-Key":         {key},
		"Anthropic-Version": {"2026-10-21"},
	}
	for turn := 0; turn < maxTurns; turn++ {
		var resp struct {
			StopReason string          `json:"stop_reason"`
			Content    json.RawMessage `json:"content"`
		}
		err := postJSON(ctx, anthropicURL, header, map[string]any{
			"model": model, "max_tokens": 16000, "messages": messages, "tools": tools,
		}, &resp)
		if err != nil {
			return "", err
		}
		// Send the assistant content back unchanged, including any thinking
		// blocks, before the tool results.
		messages = append(messages, message{Role: "assistant", Content: resp.Content})

		var blocks []adapter.AnthropicContentBlock
		if err := json.Unmarshal(resp.Content, &blocks); err != nil {
			return "", err
		}
		if resp.StopReason != "tool_use" {
			if resp.StopReason == "refusal" || resp.StopReason == "max_tokens" {
				return "", fmt.Errorf("model stopped with %q", resp.StopReason)
			}
			var text []string
			for _, b := range blocks {
				if b.Type == "text" {
					text = append(text, b.Text)
				}
			}
			return strings.Join(text, "\n"), nil
		}

		// The current tool_use blocks -> Blaze calls -> one user message with
		// every tool_result.
		calls, err := adapter.AnthropicCalls(blocks)
		if err != nil {
			return "", err
		}
		results, err := ex.Execute(ctx, calls)
		if err != nil {
			return "", err
		}
		logResults(results)
		reply, err := adapter.AnthropicToolResultMessage(results)
		if err != nil {
			return "", err
		}
		messages = append(messages, message{Role: reply.Role, Content: reply.Content})
	}
	return "", fmt.Errorf("no final answer after %d turns", maxTurns)
}

// ---------------------------------------------------------------------------

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func main() {
	log.SetFlags(0)
	provider := flag.String("provider", "both", "openai, anthropic or both")
	flag.Parse()

	type run struct {
		name, keyVar, modelVar, defaultModel string
		fn                                   func(context.Context, *adapter.Executor, string, string) (string, error)
	}
	runs := []run{
		{"openai", "OPENAI_API_KEY", "OPENAI_MODEL", defaultOpenAIModel, runOpenAI},
		{"anthropic", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", defaultAnthropicModel, runAnthropic},
	}

	failed := false
	for _, r := range runs {
		if *provider != "both" && *provider != r.name {
			continue
		}
		key := os.Getenv(r.keyVar)
		if key == "" {
			log.Printf("[%s] skipped: %s is not set", r.name, r.keyVar)
			continue
		}
		// A fresh executor per provider, so each gets its own memory store.
		ex, err := newExecutor()
		if err != nil {
			log.Fatal(err)
		}
		// The memory scope is set by this program, never by the model.
		ctx, cancel := context.WithTimeout(tool.WithMemoryScope(context.Background(), "agent-loop:"+r.name), 5*time.Minute)
		model := envOr(r.modelVar, r.defaultModel)
		log.Printf("[%s] model %s", r.name, model)
		answer, err := r.fn(ctx, ex, key, model)
		cancel()
		if err != nil {
			log.Printf("[%s] failed: %v", r.name, err)
			failed = true
			continue
		}
		fmt.Printf("[%s] %s\n", r.name, strings.TrimSpace(answer))
	}
	if failed {
		os.Exit(1)
	}
}
