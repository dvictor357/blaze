package tool

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/dvictor357/blaze/adapter"
)

// NewWebSearchTool creates a web search tool that uses DuckDuckGo.
// No API key required - it scrapes the HTML results page.
// This gives the AI the ability to search the internet for information.
func NewWebSearchTool() adapter.Tool {
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
			"required": []string{"query"},
		},
		func(input json.RawMessage) (any, error) {
			var data struct {
				Query      string `json:"query"`
				MaxResults int    `json:"max_results"`
			}
			if err := json.Unmarshal(input, &data); err != nil {
				return nil, fmt.Errorf("invalid input: %w", err)
			}

			if data.Query == "" {
				return nil, fmt.Errorf("query cannot be empty")
			}

			if data.MaxResults <= 0 {
				data.MaxResults = 5
			}
			if data.MaxResults > 10 {
				data.MaxResults = 10
			}

			results, err := searchDuckDuckGo(data.Query, data.MaxResults)
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

// SearchResult represents a single search result
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// searchDuckDuckGo performs a search using DuckDuckGo's HTML interface
func searchDuckDuckGo(query string, maxResults int) ([]SearchResult, error) {
	// Use DuckDuckGo HTML interface (no JavaScript required)
	searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(query))

	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil // Follow redirects
		},
	}

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers to look like a browser
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("search failed with status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 500*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	html := string(body)

	// Parse DuckDuckGo HTML results
	results := parseDuckDuckGoResults(html, maxResults)

	if len(results) == 0 {
		// Fallback: try alternate parsing
		results = parseDuckDuckGoResultsAlt(html, maxResults)
	}

	return results, nil
}

// parseDuckDuckGoResults extracts search results from DuckDuckGo HTML
func parseDuckDuckGoResults(html string, maxResults int) []SearchResult {
	var results []SearchResult

	// DuckDuckGo HTML uses class="result" for each result
	// Each result has:
	// - class="result__a" for the link
	// - class="result__snippet" for the description

	// Alternative: find individual components
	linkPattern := regexp.MustCompile(`(?is)<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	snippetPattern := regexp.MustCompile(`(?is)<a[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)

	links := linkPattern.FindAllStringSubmatch(html, maxResults*2)
	snippets := snippetPattern.FindAllStringSubmatch(html, maxResults*2)

	for i := 0; i < len(links) && len(results) < maxResults; i++ {
		if len(links[i]) < 3 {
			continue
		}

		rawURL := links[i][1]
		title := cleanText(links[i][2])

		// DuckDuckGo wraps URLs - extract the actual URL
		actualURL := extractActualURL(rawURL)
		if actualURL == "" {
			continue
		}

		snippet := ""
		if i < len(snippets) && len(snippets[i]) > 1 {
			snippet = cleanText(snippets[i][1])
		}

		// Skip ads and internal DDG links
		if strings.Contains(actualURL, "duckduckgo.com") {
			continue
		}

		results = append(results, SearchResult{
			Title:   title,
			URL:     actualURL,
			Snippet: snippet,
		})
	}

	// Deduplicate by URL
	seen := make(map[string]bool)
	deduped := []SearchResult{}
	for _, r := range results {
		if !seen[r.URL] {
			seen[r.URL] = true
			deduped = append(deduped, r)
		}
	}

	return deduped
}

// parseDuckDuckGoResultsAlt is a fallback parser for different HTML structures
func parseDuckDuckGoResultsAlt(html string, maxResults int) []SearchResult {
	var results []SearchResult

	// Try to find any links with titles
	pattern := regexp.MustCompile(`(?is)<a[^>]*href="(/l/\?[^"]*uddg=([^&"]+)[^"]*)"[^>]*>([^<]+)</a>`)
	matches := pattern.FindAllStringSubmatch(html, maxResults*2)

	for _, match := range matches {
		if len(match) < 4 || len(results) >= maxResults {
			continue
		}

		encodedURL := match[2]
		title := cleanText(match[3])

		actualURL, err := url.QueryUnescape(encodedURL)
		if err != nil {
			continue
		}

		if title == "" || actualURL == "" {
			continue
		}

		results = append(results, SearchResult{
			Title:   title,
			URL:     actualURL,
			Snippet: "",
		})
	}

	return results
}

// extractActualURL extracts the real URL from DuckDuckGo's redirect URL
func extractActualURL(ddgURL string) string {
	// DuckDuckGo uses URLs like: //duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com...
	if strings.Contains(ddgURL, "uddg=") {
		parsed, err := url.Parse(ddgURL)
		if err != nil {
			return ""
		}
		encoded := parsed.Query().Get("uddg")
		if encoded != "" {
			decoded, err := url.QueryUnescape(encoded)
			if err != nil {
				return encoded
			}
			return decoded
		}
	}

	// Handle direct URLs
	if strings.HasPrefix(ddgURL, "http") {
		return ddgURL
	}

	// Handle protocol-relative URLs
	if strings.HasPrefix(ddgURL, "//") {
		return "https:" + ddgURL
	}

	return ""
}

// cleanText removes HTML tags and cleans up whitespace
func cleanText(s string) string {
	// Remove HTML tags
	s = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(s, "")

	// Decode HTML entities
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&apos;", "'")

	// Clean whitespace
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)

	return s
}
