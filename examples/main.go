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

	// Register the Anthropic adapter as a POST endpoint
	// Blaze now ships with AI-native web tools:
	// - web_search: Search the web (DuckDuckGo, no API key)
	// - web_read: Read webpages as clean Markdown
	// - web_fetch: Raw HTTP fetch for APIs
	engine.POST("/chat", adapter.AnthropicAdapter(
		calculatorTool,
		weatherTool,
		tool.NewWebSearchTool(), // Search the internet
		tool.NewWebReadTool(),   // Read webpages as Markdown
		tool.NewWebFetchTool(),  // Raw HTTP fetch
	))

	// Also add a simple health check endpoint
	engine.GET("/", func(c *blaze.Context) error {
		return c.JSON(200, map[string]string{
			"status":  "ok",
			"message": "Claude Tool Server is running",
		})
	})

	fmt.Println("Starting server on :8080")
	fmt.Println("Try POSTing to /chat with Claude's tool_use format")
	engine.Listen(":8080")
}
