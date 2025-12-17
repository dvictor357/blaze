package main

import (
	"encoding/json"
	"fmt"

	"github.com/dvictor357/blaze"
	"github.com/dvictor357/blaze/adapter"
	"github.com/dvictor357/blaze/tool"
)

func main() {
	engine := blaze.New()

	// Define some tools
	calculatorTool := adapter.NewTool(
		"calculator",
		"Perform basic mathematical calculations",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"expression": map[string]any{
					"type":        "string",
					"description": "Mathematical expression to evaluate (e.g., '2 + 2')",
				},
			},
			"required": []string{"expression"},
		},
		func(input json.RawMessage) (any, error) {
			var data struct {
				Expression string `json:"expression"`
			}
			if err := json.Unmarshal(input, &data); err != nil {
				return nil, err
			}

			// Simple calculator (in real use, use a proper parser)
			var result float64
			switch data.Expression {
			case "2 + 2":
				result = 4
			case "10 / 2":
				result = 5
			case "3 * 4":
				result = 12
			default:
				return nil, fmt.Errorf("unknown expression: %s", data.Expression)
			}

			return map[string]any{
				"result":   result,
				"original": data.Expression,
			}, nil
		},
	)

	weatherTool := adapter.NewTool(
		"weather",
		"Get weather information for a location",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"location": map[string]any{
					"type":        "string",
					"description": "City name (e.g., 'New York')",
				},
			},
			"required": []string{"location"},
		},
		func(input json.RawMessage) (any, error) {
			var data struct {
				Location string `json:"location"`
			}
			if err := json.Unmarshal(input, &data); err != nil {
				return nil, err
			}

			// Mock weather data
			return map[string]any{
				"location":  data.Location,
				"temp":      "72°F",
				"condition": "Sunny",
				"humidity":  "45%",
			}, nil
		},
	)

	// Collect all tools for reuse
	allTools := []adapter.Tool{
		calculatorTool,
		weatherTool,
		// Web Tools
		tool.NewWebSearchTool(),
		tool.NewWebReadTool(),
		tool.NewWebFetchTool(),
		// Essential Tools
		tool.NewDateTimeTool(),
		tool.NewJSONQueryTool(),
		tool.NewMemoryTool(),
	}

	// Register the Anthropic adapter as a POST endpoint
	// Blaze ships with a comprehensive AI toolkit:
	//
	// Web Tools:
	// - web_search: Search the web (DuckDuckGo, no API key)
	// - web_read: Read webpages as clean Markdown
	// - web_fetch: Raw HTTP fetch for APIs
	//
	// Essential Tools:
	// - datetime: Current time, timezone conversion, date math
	// - json_query: Query/filter JSON data (jq-like)
	// - memory: In-memory key-value storage with TTL
	engine.POST("/chat", adapter.AnthropicAdapter(allTools...))

	// Register the OpenAI adapter for OpenAI-compatible clients
	engine.POST("/openai", adapter.OpenAIAdapter(allTools...))

	// Register ListTools endpoint for tool discovery
	// Returns tools in both OpenAI and Anthropic formats
	engine.GET("/tools", adapter.ListToolsHandler(allTools...))

	// Also add a simple health check endpoint
	engine.GET("/", func(c *blaze.Context) error {
		return c.JSON(200, map[string]string{
			"status":    "ok",
			"message":   "AI Tool Server is running",
			"endpoints": "/chat (Anthropic), /openai (OpenAI), /tools (List)",
		})
	})

	fmt.Println("🔥 Blaze AI Tool Server running on :8080")
	fmt.Println("Endpoints:")
	fmt.Println("  POST /chat   - Anthropic/Claude format")
	fmt.Println("  POST /openai - OpenAI format")
	fmt.Println("  GET  /tools  - List available tools")
	engine.Listen(":8080")
}
