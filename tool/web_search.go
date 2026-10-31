package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/dvictor357/blaze/adapter"
)

const webSearchMaxBody = 500 * 1024

var (
	reDDGLink    = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	reDDGSnippet = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)
	reDDGAlt     = regexp.MustCompile(`(?is)<a[^>]*href="(/l/\?[^"]*uddg=([^&"]+)[^"]*)"[^>]*>([^<]+)</a>`)
)

// NewWebSearchTool creates a web search tool that uses DuckDuckGo.
// No API key required - it scrapes the HTML results page.
// This gives the AI the ability to search the internet for information.
// Requests go through client (nil selects a new client with the default,
// deny-private-networks policy).
func NewWebSearchTool(client *WebClient) adapter.Tool {
	client = webClientOrDefault(client)
	return adapter.NewTool(
		"web_search",
		"Search the web using DuckDuckGo and return a list of results with titles, URLs, and snippets. Use this to find information, documentation, or answers to questions. No API key required.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query (e.g., 'golang http server tutorial')",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum number of results to return (default: 5, max: 10)",
				},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
		func(ctx context.Context, input json.RawMessage) (any, error) {
			var data struct {
				Query      string `json:"query"`
				MaxResults int    `json:"max_results"`
			}
			if err := decodeInput(input, &data); err != nil {
				return nil, err
			}

			data.Query = strings.TrimSpace(data.Query)
			if data.Query == "" {
				return nil, fmt.Errorf("query cannot be empty")
			}
			if len(data.Query) > 500 {
				return nil, fmt.Errorf("query is too long (max 500 bytes)")
			}

			if data.MaxResults <= 0 {
				data.MaxResults = 5
			}
			if data.MaxResults > 10 {
				data.MaxResults = 10
			}

			results, err := searchDuckDuckGo(ctx, client, searchEndpoint, data.Query, data.MaxResults)
			if err != nil {
				return nil, err
			}

			return map[string]any{
				"query":   data.Query,
				"results": results,
				"count":   len(results),
			}, nil
		},
	)
}

// searchEndpoint is the DuckDuckGo HTML search page.
const searchEndpoint = "https://html.duckduckgo.com/html/"

// SearchResult represents a single search result
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// searchDuckDuckGo performs a search using DuckDuckGo's HTML interface
func searchDuckDuckGo(ctx context.Context, client *WebClient, endpoint, query string, maxResults int) ([]SearchResult, error) {
	header := http.Header{}
	header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := client.Get(ctx, endpoint+"?q="+url.QueryEscape(query), header, webSearchMaxBody)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("search failed with status: %d", resp.Status)
	}

	doc := strings.ToValidUTF8(string(resp.Body), "")
	results := parseDuckDuckGoResults(doc, maxResults)
	if len(results) == 0 {
		// Fallback: try alternate parsing
		results = parseDuckDuckGoResultsAlt(doc, maxResults)
	}
	return results, nil
}

// parseDuckDuckGoResults extracts search results from DuckDuckGo HTML
func parseDuckDuckGoResults(doc string, maxResults int) []SearchResult {
	// DuckDuckGo HTML uses class="result__a" for the link and
	// class="result__snippet" for the description.
	links := reDDGLink.FindAllStringSubmatch(doc, maxResults*2)
	snippets := reDDGSnippet.FindAllStringSubmatch(doc, maxResults*2)

	results := []SearchResult{}
	seen := make(map[string]bool)
	for i := 0; i < len(links) && len(results) < maxResults; i++ {
		if len(links[i]) < 3 {
			continue
		}

		// DuckDuckGo wraps URLs - extract the actual URL
		actualURL := extractActualURL(html.UnescapeString(links[i][1]))
		if actualURL == "" {
			continue
		}
		// Skip ads and internal DDG links
		if strings.Contains(actualURL, "duckduckgo.com") || seen[actualURL] {
			continue
		}
		seen[actualURL] = true

		snippet := ""
		if i < len(snippets) && len(snippets[i]) > 1 {
			snippet = cleanText(snippets[i][1])
		}

		results = append(results, SearchResult{
			Title:   cleanText(links[i][2]),
			URL:     actualURL,
			Snippet: snippet,
		})
	}
	return results
}

// parseDuckDuckGoResultsAlt is a fallback parser for different HTML structures
func parseDuckDuckGoResultsAlt(doc string, maxResults int) []SearchResult {
	results := []SearchResult{}

	for _, match := range reDDGAlt.FindAllStringSubmatch(doc, maxResults*2) {
		if len(match) < 4 || len(results) >= maxResults {
			continue
		}

		title := cleanText(match[3])
		actualURL, err := url.QueryUnescape(match[2])
		if err != nil || title == "" || !isWebURL(actualURL) {
			continue
		}

		results = append(results, SearchResult{Title: title, URL: actualURL})
	}

	return results
}

// extractActualURL extracts the real URL from DuckDuckGo's redirect URL. Only
// absolute http(s) URLs are returned.
func extractActualURL(ddgURL string) string {
	// DuckDuckGo uses URLs like: //duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com...
	if strings.Contains(ddgURL, "uddg=") {
		if strings.HasPrefix(ddgURL, "//") {
			ddgURL = "https:" + ddgURL
		}
		parsed, err := url.Parse(ddgURL)
		if err != nil {
			return ""
		}
		if target := parsed.Query().Get("uddg"); target != "" && isWebURL(target) {
			return target
		}
		return ""
	}

	// Handle protocol-relative URLs
	if strings.HasPrefix(ddgURL, "//") {
		ddgURL = "https:" + ddgURL
	}
	if isWebURL(ddgURL) {
		return ddgURL
	}
	return ""
}

// isWebURL reports whether s is an acceptable absolute http(s) URL.
func isWebURL(s string) bool {
	_, err := ParseWebURL(s)
	return err == nil
}

// cleanText removes HTML tags and cleans up whitespace
func cleanText(s string) string {
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reAnyWS.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
