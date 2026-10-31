package tool

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/dvictor357/blaze/adapter"
)

const webFetchMaxBody = 50 * 1024

// NewWebFetchTool creates a basic HTTP fetcher that returns raw content.
// Use this when you need the unprocessed response (e.g., for APIs, JSON, raw data).
// For reading webpages, prefer NewWebReadTool which provides clean Markdown.
//
// Requests go through client (nil selects a new client with the default,
// deny-private-networks policy). Custom headers are validated and are not
// forwarded across cross-origin redirects.
func NewWebFetchTool(client *WebClient) adapter.Tool {
	client = webClientOrDefault(client)
	return adapter.NewTool(
		"web_fetch",
		"Fetch raw content from an http(s) URL (HTTP GET). Returns the unprocessed response body, capped at 50KB. Private, loopback and link-local addresses are blocked. Best for APIs or when you need raw data. For readable webpage content, use 'web_read' instead.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The absolute http:// or https:// URL to fetch",
				},
				"headers": map[string]any{
					"type":        "object",
					"description": "Optional custom request headers (string values; at most 16)",
				},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		},
		func(ctx context.Context, input json.RawMessage) (any, error) {
			var data struct {
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			}
			if err := decodeInput(input, &data); err != nil {
				return nil, err
			}

			header, err := sanitizeRequestHeaders(data.Headers)
			if err != nil {
				return nil, err
			}
			if header.Get("User-Agent") == "" {
				header.Set("User-Agent", "BlazeBot/1.0")
			}

			resp, err := client.Get(ctx, data.URL, header, webFetchMaxBody)
			if err != nil {
				return nil, err
			}

			// Set-Cookie is never relayed to the model.
			respHeaders := make(map[string]string, len(resp.Header))
			for k, v := range resp.Header {
				if len(v) > 0 && http.CanonicalHeaderKey(k) != "Set-Cookie" {
					respHeaders[k] = v[0]
				}
			}

			return map[string]any{
				"status":       resp.Status,
				"url":          resp.FinalURL,
				"content_type": resp.Header.Get("Content-Type"),
				"headers":      respHeaders,
				"body":         string(resp.Body),
				"size":         len(resp.Body),
				"truncated":    resp.Truncated,
			}, nil
		},
	)
}
