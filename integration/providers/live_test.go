package providers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go/v2"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/dvictor357/blaze/adapter"
	openai "github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

const toolName = "lookup_confirmation"
const prompt = "Call lookup_confirmation exactly once with item set to blue. Then respond with the exact confirmation value from the tool result, without changing it."

func livePrerequisites(t *testing.T, keyName, modelName string) (string, string) {
	t.Helper()
	if os.Getenv("BLAZE_LIVE_PROVIDERS") != "1" {
		t.Skip("set BLAZE_LIVE_PROVIDERS=1 to run paid live provider tests")
	}
	key, model := os.Getenv(keyName), strings.TrimSpace(os.Getenv(modelName))
	if strings.TrimSpace(key) == "" || model == "" {
		t.Fatalf("live provider test requires %s and %s", keyName, modelName)
	}
	return key, model
}

func newLiveExecutor(t *testing.T) (*adapter.Executor, *string) {
	t.Helper()
	var token string
	schema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"item": map[string]any{"type": "string", "enum": []string{"blue"}}},
		"required":             []string{"item"},
		"additionalProperties": false,
	}
	tool := adapter.NewTool(toolName, "Look up a confirmation for the requested item.", schema, func(ctx context.Context, raw json.RawMessage) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var input map[string]any
		if err := json.Unmarshal(raw, &input); err != nil || len(input) != 1 || input["item"] != "blue" {
			return nil, errors.New("expected exactly item=blue")
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		token = hex.EncodeToString(random[:])
		return map[string]string{"item": "blue", "confirmation": token}, nil
	})
	executor, err := adapter.NewExecutor(adapter.Config{}, tool)
	if err != nil {
		t.Fatalf("create executor: %v", err)
	}
	return executor, &token
}

func executeCall(t *testing.T, ctx context.Context, executor *adapter.Executor, calls []adapter.Call, token *string) []adapter.Result {
	t.Helper()
	if len(calls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(calls))
	}
	call := calls[0]
	if call.ID == "" || call.Name != toolName {
		t.Fatal("unexpected tool call ID or name")
	}
	var input map[string]any
	if err := json.Unmarshal(call.Input, &input); err != nil || len(input) != 1 || input["item"] != "blue" {
		t.Fatal("tool call input did not contain exactly item=blue")
	}
	results, err := executor.Execute(ctx, calls)
	if err != nil {
		t.Fatalf("execute tool: %v", err)
	}
	if len(results) != 1 || !results[0].OK || results[0].Error != nil || results[0].ID != call.ID || results[0].Name != call.Name {
		t.Fatal("tool execution did not produce one correlated successful result")
	}
	var output map[string]string
	if err := json.Unmarshal(results[0].Output, &output); err != nil || output["item"] != "blue" || *token == "" || output["confirmation"] != *token {
		t.Fatal("tool output did not contain the generated confirmation")
	}
	return results
}

func TestLiveOpenAI(t *testing.T) {
	key, model := livePrerequisites(t, "OPENAI_API_KEY", "OPENAI_MODEL")
	t.Logf("OpenAI model: %s", model)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	executor, token := newLiveExecutor(t)
	def := executor.Tools()[0].ToOpenAI()
	schema, ok := def.Function.Parameters.(map[string]any)
	if !ok || def.Type != "function" {
		t.Fatal("unexpected OpenAI tool definition")
	}
	client := openai.NewClient(
		openaioption.WithAPIKey(key),
		openaioption.WithBaseURL("https://api.openai.com/v1"),
		openaioption.WithMaxRetries(0),
	)
	params := openai.ChatCompletionNewParams{
		Model:               openai.ChatModel(model),
		MaxCompletionTokens: openai.Int(256),
		Messages:            []openai.ChatCompletionMessageParamUnion{openai.UserMessage(prompt)},
		Tools: []openai.ChatCompletionToolUnionParam{openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: def.Function.Name, Description: openai.String(def.Function.Description), Parameters: shared.FunctionParameters(schema),
		})},
		ToolChoice:        openai.ToolChoiceOptionFunctionToolChoice(openai.ChatCompletionNamedToolChoiceFunctionParam{Name: def.Function.Name}),
		ParallelToolCalls: openai.Bool(false),
	}
	first, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		t.Fatal("OpenAI first request failed")
	}
	if len(first.Choices) != 1 {
		t.Fatalf("expected one OpenAI choice, got %d", len(first.Choices))
	}
	message := first.Choices[0].Message
	providerCalls := make([]adapter.OpenAIToolCall, len(message.ToolCalls))
	for i, call := range message.ToolCalls {
		providerCalls[i] = adapter.OpenAIToolCall{ID: call.ID, Type: call.Type, Function: adapter.OpenAIFunctionCall{Name: call.Function.Name, Arguments: call.Function.Arguments}}
	}
	calls, err := adapter.OpenAICalls(providerCalls)
	if err != nil {
		t.Fatalf("convert OpenAI calls: %v", err)
	}
	results := executeCall(t, ctx, executor, calls, token)
	toolMessages, err := adapter.OpenAIToolMessages(results)
	if err != nil {
		t.Fatalf("convert OpenAI results: %v", err)
	}
	if len(toolMessages) != 1 || toolMessages[0].Role != "tool" || toolMessages[0].ToolCallID != calls[0].ID {
		t.Fatal("invalid OpenAI tool result message")
	}
	params.Messages = append(params.Messages, message.ToParam(), openai.ToolMessage(toolMessages[0].Content, toolMessages[0].ToolCallID))
	params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: openai.String("none")}
	second, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		t.Fatal("OpenAI second request failed")
	}
	if len(second.Choices) != 1 || !strings.Contains(second.Choices[0].Message.Content, *token) {
		t.Fatal("OpenAI final response did not contain the generated confirmation")
	}
}

func TestLiveAnthropic(t *testing.T) {
	key, model := livePrerequisites(t, "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL")
	t.Logf("Anthropic model: %s", model)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	executor, token := newLiveExecutor(t)
	def := executor.Tools()[0].ToAnthropic()
	schema, ok := def.InputSchema.(map[string]any)
	if !ok {
		t.Fatal("unexpected Anthropic tool definition")
	}
	properties, ok := schema["properties"]
	if !ok {
		t.Fatal("missing Anthropic schema properties")
	}
	requiredValues, ok := schema["required"].([]any)
	if !ok {
		t.Fatal("missing Anthropic schema required fields")
	}
	required := make([]string, len(requiredValues))
	for i, value := range requiredValues {
		var valid bool
		required[i], valid = value.(string)
		if !valid {
			t.Fatal("invalid Anthropic schema required field")
		}
	}
	client := anthropic.NewClient(
		anthropicoption.WithAPIKey(key),
		anthropicoption.WithBaseURL("https://api.anthropic.com"),
		anthropicoption.WithMaxRetries(0),
	)
	params := anthropic.MessageNewParams{
		Model: model, MaxTokens: 256,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))},
		Tools: []anthropic.ToolUnionParam{{OfTool: &anthropic.ToolParam{
			Name: def.Name, Description: anthropic.String(def.Description),
			InputSchema: anthropic.ToolInputSchemaParam{Properties: properties, Required: required, ExtraFields: map[string]any{"additionalProperties": schema["additionalProperties"]}},
		}}},
		ToolChoice: anthropic.ToolChoiceUnionParam{OfTool: &anthropic.ToolChoiceToolParam{Name: def.Name, DisableParallelToolUse: anthropic.Bool(true)}},
	}
	first, err := client.Messages.New(ctx, params)
	if err != nil {
		t.Fatal("Anthropic first request failed")
	}
	blocks := make([]adapter.AnthropicContentBlock, len(first.Content))
	for i, block := range first.Content {
		blocks[i] = adapter.AnthropicContentBlock{Type: block.Type, ID: block.ID, Name: block.Name, Input: block.Input, Text: block.Text}
	}
	calls, err := adapter.AnthropicCalls(blocks)
	if err != nil {
		t.Fatalf("convert Anthropic calls: %v", err)
	}
	results := executeCall(t, ctx, executor, calls, token)
	resultMessage, err := adapter.AnthropicToolResultMessage(results)
	if err != nil {
		t.Fatalf("convert Anthropic results: %v", err)
	}
	if resultMessage.Role != "user" || len(resultMessage.Content) != 1 || resultMessage.Content[0].Type != "tool_result" || resultMessage.Content[0].ToolUseID != calls[0].ID || resultMessage.Content[0].IsError {
		t.Fatal("invalid Anthropic tool result message")
	}
	result := resultMessage.Content[0]
	params.Messages = append(params.Messages, first.ToParam(), anthropic.NewUserMessage(anthropic.NewToolResultBlock(result.ToolUseID, result.Content, result.IsError)))
	params.ToolChoice = anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}
	second, err := client.Messages.New(ctx, params)
	if err != nil {
		t.Fatal("Anthropic second request failed")
	}
	var text strings.Builder
	for _, block := range second.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	if !strings.Contains(text.String(), *token) {
		t.Fatal("Anthropic final response did not contain the generated confirmation")
	}
}
