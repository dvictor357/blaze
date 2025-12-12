package tool

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dvictor357/blaze/adapter"
)

// NewWebFetchTool creates a basic HTTP fetcher that returns raw content.
// Use this when you need the unprocessed response (e.g., for APIs, JSON, raw data).
// For reading webpages, prefer NewWebReadTool which provides clean Markdown.
func NewWebFetchTool() adapter.Tool {
	return adapter.NewTool(
		"web_fetch",
		"Fetch raw content from a URL (HTTP GET). Returns unprocessed response body. Best for APIs or when you need raw data. For readable webpage content, use 'web_read' instead.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The URL to fetch",
				},
				"headers": map[string]any{
					"type":        "object",
					"description": "Optional custom headers to send with the request",
				},
			},
			"required": []string{"url"},
		},
		func(input json.RawMessage) (any, error) {
			var data struct {
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			}
			if err := json.Unmarshal(input, &data); err != nil {
				return nil, fmt.Errorf("invalid input: %w", err)
			}

			if data.URL == "" {
				return nil, fmt.Errorf("url cannot be empty")
			}
			if !strings.HasPrefix(data.URL, "http") {
				data.URL = "https://" + data.URL
			}

			client := &http.Client{Timeout: 15 * time.Second}
			req, err := http.NewRequest("GET", data.URL, nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create request: %w", err)
			}

			// Set default User-Agent
			req.Header.Set("User-Agent", "BlazeBot/1.0")

			// Apply custom headers
			for k, v := range data.Headers {
				req.Header.Set(k, v)
			}

			resp, err := client.Do(req)
			if err != nil {
				return nil, fmt.Errorf("request failed: %w", err)
			}
			defer resp.Body.Close()

			// Read body (limit to 50KB for raw fetch)
			const MaxBodySize = 50 * 1024
			body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodySize))
			if err != nil {
				return nil, fmt.Errorf("failed to read body: %w", err)
			}

			// Collect response headers
			respHeaders := make(map[string]string)
			for k, v := range resp.Header {
				if len(v) > 0 {
					respHeaders[k] = v[0]
				}
			}

			return map[string]any{
				"status":       resp.StatusCode,
				"url":          data.URL,
				"content_type": resp.Header.Get("Content-Type"),
				"headers":      respHeaders,
				"body":         string(body),
				"size":         len(body),
				"truncated":    len(body) >= MaxBodySize,
			}, nil
		},
	)
}
