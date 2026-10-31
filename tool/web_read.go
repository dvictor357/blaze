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
	"unicode/utf8"

	"github.com/dvictor357/blaze/adapter"
)

const (
	webReadMaxBody    = 500 * 1024
	webReadMaxContent = 8 * 1024
	webReadMaxLinks   = 20
)

// Regular expressions are compiled once, not per request or per link.
var (
	reScript    = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	reStyle     = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	reNoscript  = regexp.MustCompile(`(?is)<noscript[^>]*>.*?</noscript>`)
	reComment   = regexp.MustCompile(`(?is)<!--.*?-->`)
	reBody      = regexp.MustCompile(`(?is)<body[^>]*>(.*?)</body>`)
	reNav       = regexp.MustCompile(`(?is)<nav[^>]*>.*?</nav>`)
	reHeader    = regexp.MustCompile(`(?is)<header[^>]*>.*?</header>`)
	reFooter    = regexp.MustCompile(`(?is)<footer[^>]*>.*?</footer>`)
	reAside     = regexp.MustCompile(`(?is)<aside[^>]*>.*?</aside>`)
	reForm      = regexp.MustCompile(`(?is)<form[^>]*>.*?</form>`)
	reTag       = regexp.MustCompile(`<[^>]+>`)
	reBlankRuns = regexp.MustCompile(`\n{3,}`)
	reSpaceRuns = regexp.MustCompile(`[ \t]+`)
	reAnyWS     = regexp.MustCompile(`\s+`)

	reTitle           = regexp.MustCompile(`(?is)<title[^>]*>([^<]+)</title>`)
	reMetaDescription = regexp.MustCompile(`(?i)<meta[^>]*name="description"[^>]*content="([^"]*)"`)
	reOGTitle         = regexp.MustCompile(`(?i)<meta[^>]*property="og:title"[^>]*content="([^"]*)"`)
	reOGDescription   = regexp.MustCompile(`(?i)<meta[^>]*property="og:description"[^>]*content="([^"]*)"`)
	reAnchor          = regexp.MustCompile(`(?is)<a\s[^>]*?href="([^"]*)"[^>]*>(.*?)</a>`)

	reMainPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<main[^>]*>(.*?)</main>`),
		regexp.MustCompile(`(?is)<article[^>]*>(.*?)</article>`),
		regexp.MustCompile(`(?is)<div[^>]*class="[^"]*content[^"]*"[^>]*>(.*?)</div>`),
		regexp.MustCompile(`(?is)<div[^>]*id="content"[^>]*>(.*?)</div>`),
		regexp.MustCompile(`(?is)<div[^>]*class="[^"]*post[^"]*"[^>]*>(.*?)</div>`),
	}

	mdRules = []mdRule{
		// Code blocks first, so inline <code> handling does not break them.
		{regexp.MustCompile(`(?is)<pre(?:\s[^>]*)?><code(?:\s[^>]*)?>(.*?)</code></pre>`), "\n```\n$1\n```\n"},
		tagRule("pre", "\n```\n$1\n```\n"),
		tagRule("h1", "\n# $1\n"),
		tagRule("h2", "\n## $1\n"),
		tagRule("h3", "\n### $1\n"),
		tagRule("h4", "\n#### $1\n"),
		tagRule("h5", "\n##### $1\n"),
		tagRule("h6", "\n###### $1\n"),
		tagRule("strong", "**$1**"),
		tagRule("b", "**$1**"),
		tagRule("em", "*$1*"),
		tagRule("i", "*$1*"),
		tagRule("code", "`$1`"),
		{regexp.MustCompile(`(?is)<a\s[^>]*?href="([^"]*)"[^>]*>(.*?)</a>`), "[$2]($1)"},
		{regexp.MustCompile(`(?is)<img\s[^>]*?src="([^"]*)"[^>]*?alt="([^"]*)"[^>]*>`), "![$2]($1)"},
		{regexp.MustCompile(`(?is)<img\s[^>]*?alt="([^"]*)"[^>]*?src="([^"]*)"[^>]*>`), "![$1]($2)"},
		tagRule("li", "- $1\n"),
		tagRule("ul", "$1\n"),
		tagRule("ol", "$1\n"),
		tagRule("p", "$1\n\n"),
		{regexp.MustCompile(`(?is)<br\s*/?>`), "\n"},
		{regexp.MustCompile(`(?is)<hr\s*/?>`), "\n---\n"},
		tagRule("blockquote", "> $1\n"),
	}
)

type mdRule struct {
	re   *regexp.Regexp
	repl string
}

// tagRule matches <name ...>content</name> without also matching tags whose
// names merely start with name (<b> vs <br>, <p> vs <pre>, <i> vs <img>).
func tagRule(name, repl string) mdRule {
	return mdRule{regexp.MustCompile(`(?is)<` + name + `(?:\s[^>]*)?>(.*?)</` + name + `>`), repl}
}

// NewWebReadTool creates an AI-native web reader that:
// 1. Fetches the URL
// 2. Extracts the main content (removes nav, ads, footers)
// 3. Converts HTML to clean Markdown
// 4. Extracts metadata (title, description, links)
//
// This saves tokens and gives the AI readable content instead of HTML soup.
// Requests go through client (nil selects a new client with the default,
// deny-private-networks policy).
func NewWebReadTool(client *WebClient) adapter.Tool {
	client = webClientOrDefault(client)
	return adapter.NewTool(
		"web_read",
		"Read a webpage and return clean, readable content in Markdown format. Extracts the main article content, removes navigation/ads/clutter, and provides metadata. Only absolute http:// or https:// URLs are supported, and private, loopback and link-local addresses are blocked.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The absolute URL to read (e.g., 'https://example.com/article')",
				},
			},
			"required": []string{"url"},
		},
		func(ctx context.Context, input json.RawMessage) (any, error) {
			var data struct {
				URL string `json:"url"`
			}
			if err := decodeInput(input, &data); err != nil {
				return nil, err
			}

			header := http.Header{}
			header.Set("User-Agent", "Mozilla/5.0 (compatible; BlazeBot/1.0; +https://github.com/dvictor357/blaze)")
			header.Set("Accept", "text/html,application/xhtml+xml")

			resp, err := client.Get(ctx, data.URL, header, webReadMaxBody)
			if err != nil {
				return nil, fmt.Errorf("failed to fetch: %w", err)
			}

			doc := strings.ToValidUTF8(string(resp.Body), "")

			title := cleanMeta(firstSubmatch(reTitle, doc))
			description := cleanMeta(firstSubmatch(reMetaDescription, doc))
			if title == "" {
				title = cleanMeta(firstSubmatch(reOGTitle, doc))
			}
			if description == "" {
				description = cleanMeta(firstSubmatch(reOGDescription, doc))
			}

			markdown := htmlToMarkdown(extractMainContent(doc))
			links := extractLinks(doc, resp.FinalURL)

			// Truncate markdown to preserve context window
			truncated := resp.Truncated
			if len(markdown) > webReadMaxContent {
				markdown = truncateUTF8(markdown, webReadMaxContent) + "\n\n[Content truncated...]"
				truncated = true
			}

			return map[string]any{
				"url":         resp.FinalURL,
				"title":       title,
				"description": description,
				"content":     markdown,
				"links":       links,
				"truncated":   truncated,
				"status":      resp.Status,
			}, nil
		},
	)
}

// extractMainContent removes navigation, scripts, styles, and extracts the main content
func extractMainContent(doc string) string {
	doc = reScript.ReplaceAllString(doc, "")
	doc = reStyle.ReplaceAllString(doc, "")
	doc = reNoscript.ReplaceAllString(doc, "")
	doc = reComment.ReplaceAllString(doc, "")

	for _, re := range reMainPatterns {
		if m := re.FindStringSubmatch(doc); len(m) > 1 {
			return m[1]
		}
	}

	// Fallback: extract body content and drop common non-content elements
	if m := reBody.FindStringSubmatch(doc); len(m) > 1 {
		body := m[1]
		for _, re := range []*regexp.Regexp{reNav, reHeader, reFooter, reAside, reForm} {
			body = re.ReplaceAllString(body, "")
		}
		return body
	}

	return doc
}

// htmlToMarkdown converts HTML to Markdown
func htmlToMarkdown(doc string) string {
	md := doc
	for _, rule := range mdRules {
		md = rule.re.ReplaceAllString(md, rule.repl)
	}
	md = reTag.ReplaceAllString(md, "")
	md = html.UnescapeString(md)
	md = reBlankRuns.ReplaceAllString(md, "\n\n")
	md = reSpaceRuns.ReplaceAllString(md, " ")
	return strings.TrimSpace(md)
}

func firstSubmatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); len(m) > 1 {
		return m[1]
	}
	return ""
}

func cleanMeta(s string) string {
	return strings.TrimSpace(html.UnescapeString(s))
}

// extractLinks extracts http(s) links from the page with their text. Relative
// links are resolved against baseURL; if the base is unusable no links are
// returned.
func extractLinks(doc, baseURL string) []map[string]string {
	base, err := url.Parse(baseURL)
	if err != nil || base == nil {
		return nil
	}

	var links []map[string]string
	seen := make(map[string]bool)

	for _, match := range reAnchor.FindAllStringSubmatch(doc, 200) {
		if len(match) < 3 {
			continue
		}

		href := strings.TrimSpace(html.UnescapeString(match[1]))
		text := strings.TrimSpace(reAnyWS.ReplaceAllString(reTag.ReplaceAllString(match[2], ""), " "))

		// Skip empty and anchor-only links
		if href == "" || strings.HasPrefix(href, "#") {
			continue
		}

		resolved, err := base.Parse(href)
		if err != nil {
			continue
		}
		if resolved.Scheme != "http" && resolved.Scheme != "https" {
			continue // javascript:, mailto:, data: ...
		}
		resolved.Fragment = ""
		href = resolved.String()

		if seen[href] {
			continue
		}
		seen[href] = true

		if len(text) > 100 {
			text = truncateUTF8(text, 100) + "..."
		}

		links = append(links, map[string]string{"url": href, "text": text})
		if len(links) >= webReadMaxLinks {
			break
		}
	}

	return links
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
