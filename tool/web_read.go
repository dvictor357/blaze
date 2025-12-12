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

// NewWebReadTool creates an AI-native web reader that:
// 1. Fetches the URL
// 2. Extracts the main content (removes nav, ads, footers)
// 3. Converts HTML to clean Markdown
// 4. Extracts metadata (title, description, links)
//
// This saves tokens and gives the AI readable content instead of HTML soup.
func NewWebReadTool() adapter.Tool {
	return adapter.NewTool(
		"web_read",
		"Read a webpage and return clean, readable content in Markdown format. Extracts the main article content, removes navigation/ads/clutter, and provides metadata. Use this to read documentation, articles, or any webpage.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The URL to read (e.g., 'https://example.com/article')",
				},
			},
			"required": []string{"url"},
		},
		func(input json.RawMessage) (any, error) {
			var data struct {
				URL string `json:"url"`
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

			// Fetch the page
			client := &http.Client{Timeout: 15 * time.Second}
			req, _ := http.NewRequest("GET", data.URL, nil)
			req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; BlazeBot/1.0; +https://github.com/dvictor357/blaze)")
			req.Header.Set("Accept", "text/html,application/xhtml+xml")

			resp, err := client.Do(req)
			if err != nil {
				return nil, fmt.Errorf("failed to fetch: %w", err)
			}
			defer resp.Body.Close()

			// Limit to 500KB to prevent memory issues
			body, err := io.ReadAll(io.LimitReader(resp.Body, 500*1024))
			if err != nil {
				return nil, fmt.Errorf("failed to read body: %w", err)
			}

			html := string(body)

			// Extract metadata
			title := extractMeta(html, `<title[^>]*>([^<]+)</title>`)
			description := extractMetaTag(html, "description")
			ogTitle := extractMetaProperty(html, "og:title")
			ogDesc := extractMetaProperty(html, "og:description")

			if title == "" {
				title = ogTitle
			}
			if description == "" {
				description = ogDesc
			}

			// Extract and clean main content
			content := extractMainContent(html)
			markdown := htmlToMarkdown(content)

			// Extract links from the page
			links := extractLinks(html, data.URL)

			// Truncate markdown to preserve context window (max 8KB)
			const MaxContentSize = 8 * 1024
			truncated := false
			if len(markdown) > MaxContentSize {
				markdown = markdown[:MaxContentSize] + "\n\n[Content truncated...]"
				truncated = true
			}

			return map[string]any{
				"url":         data.URL,
				"title":       title,
				"description": description,
				"content":     markdown,
				"links":       links,
				"truncated":   truncated,
				"status":      resp.StatusCode,
			}, nil
		},
	)
}

// extractMainContent removes navigation, scripts, styles, and extracts the main content
func extractMainContent(html string) string {
	// Remove scripts and styles
	html = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`).ReplaceAllString(html, "")
	html = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`).ReplaceAllString(html, "")
	html = regexp.MustCompile(`(?is)<noscript[^>]*>.*?</noscript>`).ReplaceAllString(html, "")
	html = regexp.MustCompile(`(?is)<!--.*?-->`).ReplaceAllString(html, "")

	// Try to find main content areas (common patterns)
	mainPatterns := []string{
		`(?is)<main[^>]*>(.*?)</main>`,
		`(?is)<article[^>]*>(.*?)</article>`,
		`(?is)<div[^>]*class="[^"]*content[^"]*"[^>]*>(.*?)</div>`,
		`(?is)<div[^>]*id="content"[^>]*>(.*?)</div>`,
		`(?is)<div[^>]*class="[^"]*post[^"]*"[^>]*>(.*?)</div>`,
	}

	for _, pattern := range mainPatterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(html); len(matches) > 1 {
			return matches[1]
		}
	}

	// Fallback: extract body content
	bodyRe := regexp.MustCompile(`(?is)<body[^>]*>(.*?)</body>`)
	if matches := bodyRe.FindStringSubmatch(html); len(matches) > 1 {
		body := matches[1]
		// Remove common non-content elements
		body = regexp.MustCompile(`(?is)<nav[^>]*>.*?</nav>`).ReplaceAllString(body, "")
		body = regexp.MustCompile(`(?is)<header[^>]*>.*?</header>`).ReplaceAllString(body, "")
		body = regexp.MustCompile(`(?is)<footer[^>]*>.*?</footer>`).ReplaceAllString(body, "")
		body = regexp.MustCompile(`(?is)<aside[^>]*>.*?</aside>`).ReplaceAllString(body, "")
		body = regexp.MustCompile(`(?is)<form[^>]*>.*?</form>`).ReplaceAllString(body, "")
		return body
	}

	return html
}

// htmlToMarkdown converts HTML to Markdown
func htmlToMarkdown(html string) string {
	md := html

	// Convert headings
	md = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`).ReplaceAllString(md, "\n# $1\n")
	md = regexp.MustCompile(`(?is)<h2[^>]*>(.*?)</h2>`).ReplaceAllString(md, "\n## $1\n")
	md = regexp.MustCompile(`(?is)<h3[^>]*>(.*?)</h3>`).ReplaceAllString(md, "\n### $1\n")
	md = regexp.MustCompile(`(?is)<h4[^>]*>(.*?)</h4>`).ReplaceAllString(md, "\n#### $1\n")
	md = regexp.MustCompile(`(?is)<h5[^>]*>(.*?)</h5>`).ReplaceAllString(md, "\n##### $1\n")
	md = regexp.MustCompile(`(?is)<h6[^>]*>(.*?)</h6>`).ReplaceAllString(md, "\n###### $1\n")

	// Convert formatting
	md = regexp.MustCompile(`(?is)<strong[^>]*>(.*?)</strong>`).ReplaceAllString(md, "**$1**")
	md = regexp.MustCompile(`(?is)<b[^>]*>(.*?)</b>`).ReplaceAllString(md, "**$1**")
	md = regexp.MustCompile(`(?is)<em[^>]*>(.*?)</em>`).ReplaceAllString(md, "*$1*")
	md = regexp.MustCompile(`(?is)<i[^>]*>(.*?)</i>`).ReplaceAllString(md, "*$1*")
	md = regexp.MustCompile(`(?is)<code[^>]*>(.*?)</code>`).ReplaceAllString(md, "`$1`")

	// Convert links
	md = regexp.MustCompile(`(?is)<a[^>]*href="([^"]*)"[^>]*>(.*?)</a>`).ReplaceAllString(md, "[$2]($1)")

	// Convert images
	md = regexp.MustCompile(`(?is)<img[^>]*src="([^"]*)"[^>]*alt="([^"]*)"[^>]*/?>`).ReplaceAllString(md, "![$2]($1)")
	md = regexp.MustCompile(`(?is)<img[^>]*alt="([^"]*)"[^>]*src="([^"]*)"[^>]*/?>`).ReplaceAllString(md, "![$1]($2)")

	// Convert lists
	md = regexp.MustCompile(`(?is)<li[^>]*>(.*?)</li>`).ReplaceAllString(md, "- $1\n")
	md = regexp.MustCompile(`(?is)<ul[^>]*>(.*?)</ul>`).ReplaceAllString(md, "$1\n")
	md = regexp.MustCompile(`(?is)<ol[^>]*>(.*?)</ol>`).ReplaceAllString(md, "$1\n")

	// Convert paragraphs and line breaks
	md = regexp.MustCompile(`(?is)<p[^>]*>(.*?)</p>`).ReplaceAllString(md, "$1\n\n")
	md = regexp.MustCompile(`(?is)<br\s*/?>`).ReplaceAllString(md, "\n")
	md = regexp.MustCompile(`(?is)<hr\s*/?>`).ReplaceAllString(md, "\n---\n")

	// Convert blockquotes
	md = regexp.MustCompile(`(?is)<blockquote[^>]*>(.*?)</blockquote>`).ReplaceAllString(md, "> $1\n")

	// Convert pre/code blocks
	md = regexp.MustCompile(`(?is)<pre[^>]*><code[^>]*>(.*?)</code></pre>`).ReplaceAllString(md, "\n```\n$1\n```\n")
	md = regexp.MustCompile(`(?is)<pre[^>]*>(.*?)</pre>`).ReplaceAllString(md, "\n```\n$1\n```\n")

	// Remove remaining HTML tags
	md = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(md, "")

	// Decode common HTML entities
	md = strings.ReplaceAll(md, "&nbsp;", " ")
	md = strings.ReplaceAll(md, "&amp;", "&")
	md = strings.ReplaceAll(md, "&lt;", "<")
	md = strings.ReplaceAll(md, "&gt;", ">")
	md = strings.ReplaceAll(md, "&quot;", "\"")
	md = strings.ReplaceAll(md, "&#39;", "'")
	md = strings.ReplaceAll(md, "&apos;", "'")

	// Clean up whitespace
	md = regexp.MustCompile(`\n{3,}`).ReplaceAllString(md, "\n\n")
	md = regexp.MustCompile(`[ \t]+`).ReplaceAllString(md, " ")
	md = strings.TrimSpace(md)

	return md
}

// extractMeta extracts content matching a regex pattern
func extractMeta(html, pattern string) string {
	re := regexp.MustCompile(pattern)
	if matches := re.FindStringSubmatch(html); len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

// extractMetaTag extracts content from <meta name="..." content="...">
func extractMetaTag(html, name string) string {
	pattern := fmt.Sprintf(`(?i)<meta[^>]*name="%s"[^>]*content="([^"]*)"`, name)
	return extractMeta(html, pattern)
}

// extractMetaProperty extracts content from <meta property="..." content="...">
func extractMetaProperty(html, property string) string {
	pattern := fmt.Sprintf(`(?i)<meta[^>]*property="%s"[^>]*content="([^"]*)"`, property)
	return extractMeta(html, pattern)
}

// extractLinks extracts all links from the page with their text
func extractLinks(html, baseURL string) []map[string]string {
	var links []map[string]string
	seen := make(map[string]bool)

	re := regexp.MustCompile(`(?is)<a[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	matches := re.FindAllStringSubmatch(html, -1)

	base, _ := url.Parse(baseURL)

	for _, match := range matches {
		if len(match) < 3 {
			continue
		}

		href := strings.TrimSpace(match[1])
		text := strings.TrimSpace(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(match[2], ""))

		// Skip empty, javascript, or anchor-only links
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") {
			continue
		}

		// Resolve relative URLs
		if !strings.HasPrefix(href, "http") {
			if parsed, err := base.Parse(href); err == nil {
				href = parsed.String()
			}
		}

		// Skip duplicates
		if seen[href] {
			continue
		}
		seen[href] = true

		// Limit text length
		if len(text) > 100 {
			text = text[:100] + "..."
		}

		links = append(links, map[string]string{
			"url":  href,
			"text": text,
		})

		// Limit to 20 links to save tokens
		if len(links) >= 20 {
			break
		}
	}

	return links
}
