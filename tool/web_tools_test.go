package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dvictor357/blaze/adapter"
)

// callTool invokes a tool handler directly (no executor, so a panic would
// fail the test instead of being recovered) and returns its result as
// generic JSON.
func callTool(t *testing.T, tl adapter.Tool, ctx context.Context, input any) (map[string]any, error) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tl.Handler(ctx, raw)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("result is not JSON-encodable: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("result is not an object: %v", err)
	}
	return m, nil
}

func allowLoopback() *WebClient {
	return NewWebClient(WebClientConfig{AllowedNetworks: loopbackOnly})
}

func TestWebTools_MalformedURLsReturnErrorsNotPanics(t *testing.T) {
	inputs := []string{"", "http://%", "http://", "https://", "%", "http://[::1", "ftp://x", "javascript:alert(1)", "//host", "host.example", "http://u:p@host/"}
	tools := map[string]adapter.Tool{
		"web_read":  NewWebReadTool(nil),
		"web_fetch": NewWebFetchTool(nil),
	}
	for name, tl := range tools {
		for _, in := range inputs {
			if _, err := callTool(t, tl, context.Background(), map[string]string{"url": in}); err == nil {
				t.Errorf("%s accepted url %q", name, in)
			}
		}
	}
}

func TestWebTools_DefaultPolicyBlocksInternalServices(t *testing.T) {
	hits := 0
	srv, _ := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, "internal")
	}))

	for name, tl := range map[string]adapter.Tool{"web_read": NewWebReadTool(nil), "web_fetch": NewWebFetchTool(nil)} {
		if _, err := callTool(t, tl, context.Background(), map[string]string{"url": srv.URL}); err == nil {
			t.Errorf("%s reached a loopback server with the default client", name)
		}
	}
	if hits != 0 {
		t.Errorf("internal server was hit %d time(s)", hits)
	}
}

const readFixture = `<!doctype html>
<html><head>
<title>  Test &amp; Page </title>
<meta name="description" content="A &quot;test&quot; page">
<script>var secret = "<a href='http://evil.example/'>x</a>";</script>
<style>p { color: red }</style>
</head><body>
<nav><a href="/nav-only">nav</a></nav>
<main>
<h1>Heading</h1>
<p>Some <strong>bold</strong> and <em>italic</em> text with <code>code</code>.</p>
<pre><code>line1
line2</code></pre>
<ul><li>one</li><li>two</li></ul>
<img src="/pic.png" alt="a picture">
<br><b>tail</b>
<a href="/relative">Relative link</a>
<a href="/relative">Duplicate</a>
<a href="javascript:alert(1)">js</a>
<a href="mailto:someone@example.com">mail</a>
<a href="#frag">frag</a>
<a href="https://other.example/page#section">Absolute</a>
<a href="">empty</a>
</main>
<footer>footer text</footer>
</body></html>`

func TestWebRead_ExtractsReadableContent(t *testing.T) {
	srv, _ := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, readFixture)
	}))

	out, err := callTool(t, NewWebReadTool(allowLoopback()), context.Background(), map[string]string{"url": srv.URL + "/dir/page"})
	if err != nil {
		t.Fatal(err)
	}
	if out["title"] != "Test & Page" || out["description"] != `A "test" page` || out["status"] != float64(200) {
		t.Errorf("metadata = %v / %v / %v", out["title"], out["description"], out["status"])
	}

	content, _ := out["content"].(string)
	for _, want := range []string{
		"# Heading", "**bold**", "*italic*", "`code`", "```\nline1\nline2\n```", "- one", "- two",
		"![a picture](/pic.png)", "**tail**", "[Relative link](/relative)",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("content lacks %q:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"secret", "color: red", "footer text", "nav", "<"} {
		if strings.Contains(content, unwanted) {
			t.Errorf("content should not contain %q:\n%s", unwanted, content)
		}
	}

	links, _ := out["links"].([]any)
	var urls []string
	for _, l := range links {
		urls = append(urls, l.(map[string]any)["url"].(string))
	}
	// Links come from the whole page (navigation included), resolved against
	// the page URL, deduplicated, http(s) only and without fragments.
	want := []string{srv.URL + "/nav-only", srv.URL + "/relative", "https://other.example/page"}
	if strings.Join(urls, "|") != strings.Join(want, "|") {
		t.Errorf("links = %v, want %v", urls, want)
	}
}

func TestExtractLinks(t *testing.T) {
	if got := extractLinks(`<a href="/x">x</a>`, "http://[::1"); got != nil {
		t.Errorf("unusable base URL should yield no links, got %v", got)
	}

	var page strings.Builder
	for i := range 50 {
		fmt.Fprintf(&page, `<a href="/p%d">link %d</a>`, i, i)
	}
	if got := extractLinks(page.String(), "http://example.com/"); len(got) != webReadMaxLinks {
		t.Errorf("got %d links, want the cap of %d", len(got), webReadMaxLinks)
	}

	long := `<a href="/l">` + strings.Repeat("é", 200) + `</a>`
	got := extractLinks(long, "http://example.com/")
	if len(got) != 1 || !utf8.ValidString(got[0]["text"]) {
		t.Errorf("long link text must be cut on a rune boundary: %v", got)
	}
}

func TestWebRead_TruncationKeepsValidUTF8(t *testing.T) {
	srv, _ := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><body><p>"+strings.Repeat("é", 9000)+"</p></body></html>")
	}))

	out, err := callTool(t, NewWebReadTool(allowLoopback()), context.Background(), map[string]string{"url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	content := out["content"].(string)
	if out["truncated"] != true || !strings.HasSuffix(content, "[Content truncated...]") || !utf8.ValidString(content) {
		t.Errorf("truncated=%v valid=%v tail=%q", out["truncated"], utf8.ValidString(content), content[len(content)-30:])
	}
}

func TestHTMLToMarkdown_TagsAreNotConfusedWithLookalikes(t *testing.T) {
	// <pre> must not be eaten by the <p> rule, <br> by <b>, <img> by <i>.
	md := htmlToMarkdown(`<p>para</p><pre>raw block</pre><br><img src="a.png" alt="alt"><i>it</i>`)
	for _, want := range []string{"para", "```\nraw block\n```", "![alt](a.png)", "*it*"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func TestWebFetch(t *testing.T) {
	var gotAccept, gotUA string
	srv, _ := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept, gotUA = r.Header.Get("Accept"), r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=secret")
		w.Header().Set("X-Reply", "yes")
		if r.URL.Path == "/big" {
			fmt.Fprint(w, strings.Repeat("x", 60*1024))
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	tl := NewWebFetchTool(allowLoopback())

	out, err := callTool(t, tl, context.Background(), map[string]any{
		"url":     srv.URL + "/data",
		"headers": map[string]string{"accept": "application/json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["status"] != float64(200) || out["body"] != `{"ok":true}` || out["content_type"] != "application/json" || out["truncated"] != false {
		t.Errorf("fetch result = %v", out)
	}
	if gotAccept != "application/json" || gotUA != "BlazeBot/1.0" {
		t.Errorf("request headers: Accept=%q User-Agent=%q", gotAccept, gotUA)
	}
	headers := out["headers"].(map[string]any)
	if _, leaked := headers["Set-Cookie"]; leaked || headers["X-Reply"] != "yes" {
		t.Errorf("response headers = %v (Set-Cookie must not be relayed)", headers)
	}

	out, err = callTool(t, tl, context.Background(), map[string]any{"url": srv.URL + "/big"})
	if err != nil || out["truncated"] != true || out["size"] != float64(webFetchMaxBody) {
		t.Errorf("large body: %v %v", out, err)
	}
}

func TestWebFetch_RejectsBadInput(t *testing.T) {
	tl := NewWebFetchTool(allowLoopback())
	inputs := []map[string]any{
		{"url": "http://127.0.0.1:1/", "headers": map[string]string{"Host": "evil"}},
		{"url": "http://127.0.0.1:1/", "headers": map[string]string{"Proxy-Authorization": "x"}},
		{"url": "http://127.0.0.1:1/", "proxy": "http://attacker.example"}, // unknown field
		{"url": 42},
	}
	for _, in := range inputs {
		if _, err := callTool(t, tl, context.Background(), in); err == nil {
			t.Errorf("input %v accepted", in)
		}
	}
	if _, err := tl.Handler(context.Background(), json.RawMessage(`{"url":"http://127.0.0.1:1/"} trailing`)); err == nil {
		t.Error("trailing data accepted")
	}
}

func TestWebTools_HonorContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv, _ := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tl := range map[string]adapter.Tool{
		"web_read":  NewWebReadTool(allowLoopback()),
		"web_fetch": NewWebFetchTool(allowLoopback()),
	} {
		if _, err := callTool(t, tl, ctx, map[string]string{"url": srv.URL}); err == nil {
			t.Errorf("%s ignored a canceled context", name)
		}
	}
}

const searchFixture = `<html><body>
<div><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc&amp;rut=abc">The <b>Go</b> Docs</a>
<a class="result__snippet" href="x">Learn &amp; build</a></div>
<div><a class="result__a" href="//duckduckgo.com/l/?uddg=javascript%3Aalert(1)">bad</a>
<a class="result__snippet" href="x">ignored</a></div>
<div><a class="result__a" href="https://example.org/page">Example</a>
<a class="result__snippet" href="x">second snippet</a></div>
<div><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc">duplicate</a>
<a class="result__snippet" href="x">dup</a></div>
<div><a class="result__a" href="https://duckduckgo.com/y.js?ad=1">ad</a>
<a class="result__snippet" href="x">ad</a></div>
</body></html>`

func TestSearchDuckDuckGo(t *testing.T) {
	var gotQuery, gotUA string
	status := http.StatusOK
	srv, _ := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotUA = r.URL.Query().Get("q"), r.Header.Get("User-Agent")
		w.WriteHeader(status)
		fmt.Fprint(w, searchFixture)
	}))

	results, err := searchDuckDuckGo(context.Background(), allowLoopback(), srv.URL+"/html/", "go lang & more", 5)
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "go lang & more" || gotUA == "" {
		t.Errorf("query=%q ua=%q", gotQuery, gotUA)
	}
	want := []SearchResult{
		{Title: "The Go Docs", URL: "https://go.dev/doc", Snippet: "Learn & build"},
		{Title: "Example", URL: "https://example.org/page", Snippet: "second snippet"},
	}
	if len(results) != len(want) {
		t.Fatalf("results = %+v", results)
	}
	for i := range want {
		if results[i] != want[i] {
			t.Errorf("result %d = %+v, want %+v", i, results[i], want[i])
		}
	}

	limited, _ := searchDuckDuckGo(context.Background(), allowLoopback(), srv.URL+"/html/", "q", 1)
	if len(limited) != 1 {
		t.Errorf("max results ignored: %d", len(limited))
	}

	status = http.StatusTooManyRequests
	if _, err := searchDuckDuckGo(context.Background(), allowLoopback(), srv.URL+"/html/", "q", 5); err == nil {
		t.Error("a non-200 search response must be an error")
	}
}

func TestWebSearch_ValidatesInput(t *testing.T) {
	tl := NewWebSearchTool(allowLoopback())
	for _, in := range []map[string]any{
		{"query": ""},
		{"query": "   "},
		{"query": strings.Repeat("q", 600)},
		{"query": "ok", "max_results": "five"},
		{"query": "ok", "region": "us"},
	} {
		if _, err := callTool(t, tl, context.Background(), in); err == nil {
			t.Errorf("input %v accepted", in)
		}
	}
}

func TestExtractActualURL(t *testing.T) {
	tests := map[string]string{
		"//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa": "https://example.com/a",
		"https://example.com/direct":                             "https://example.com/direct",
		"//example.com/proto-relative":                           "https://example.com/proto-relative",
		"//duckduckgo.com/l/?uddg=javascript%3Aalert(1)":         "",
		"//duckduckgo.com/l/?uddg=file%3A%2F%2F%2Fetc%2Fpasswd":  "",
		"javascript:alert(1)":                                    "",
		"/relative":                                              "",
		"":                                                       "",
	}
	for in, want := range tests {
		if got := extractActualURL(in); got != want {
			t.Errorf("extractActualURL(%q) = %q, want %q", in, got, want)
		}
	}
}
