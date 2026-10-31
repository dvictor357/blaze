package tool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeResolver answers host lookups from a table and counts them, so tests
// never touch real DNS and can model rebinding.
type fakeResolver struct {
	mu      sync.Mutex
	answers map[string][]netip.Addr
	calls   map[string]int
}

func newFakeResolver(answers map[string][]string) *fakeResolver {
	r := &fakeResolver{answers: map[string][]netip.Addr{}, calls: map[string]int{}}
	for host, ips := range answers {
		for _, ip := range ips {
			r.answers[host] = append(r.answers[host], netip.MustParseAddr(ip))
		}
	}
	return r
}

func (r *fakeResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[host]++
	if addrs, ok := r.answers[host]; ok {
		return append([]netip.Addr(nil), addrs...), nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (r *fakeResolver) lookups(host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[host]
}

var loopbackOnly = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}

// testServer starts an HTTP server on loopback and returns it with its port.
func testServer(t *testing.T, h http.Handler) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return srv, u.Port()
}

func TestBlockedWebAddr(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.8.9.10", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "0.0.0.0", "0.1.2.3", "100.64.0.1", "224.0.0.1", "239.255.255.250",
		"255.255.255.255", "240.0.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.7", "203.0.113.9",
		"::1", "::", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1", "fec0::1",
		"64:ff9b::7f00:1", "2002:7f00:1::", "2001:0:4136:e378::1", "2001:db8::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254",
	}
	for _, s := range blocked {
		if a := netip.MustParseAddr(s).Unmap(); !blockedWebAddr(a) {
			t.Errorf("%s must be blocked", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "172.32.0.1", "2606:4700:4700::1111", "2a00:1450:4001:81b::200e"}
	for _, s := range allowed {
		if a := netip.MustParseAddr(s).Unmap(); blockedWebAddr(a) {
			t.Errorf("%s must be allowed", s)
		}
	}
}

func TestWebClient_BlocksDirectPrivateTargets(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	_, port := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		fmt.Fprint(w, "internal secret")
	}))

	client := NewWebClient(WebClientConfig{Timeout: 3 * time.Second})
	targets := []string{
		"http://127.0.0.1:" + port + "/",
		"http://127.1.2.3:" + port + "/",
		"http://[::1]:" + port + "/",
		"http://[::ffff:127.0.0.1]:" + port + "/",
		"http://0.0.0.0:" + port + "/",
		"http://10.0.0.1/",
		"http://192.168.0.10:8080/admin",
		"http://169.254.169.254/latest/meta-data/",
		"http://[fe80::1]/",
	}
	for _, target := range targets {
		_, err := client.Get(context.Background(), target, nil, 0)
		if !errors.Is(err, ErrWebBlocked) {
			t.Errorf("Get(%s) error = %v, want ErrWebBlocked", target, err)
		}
	}

	// Names that resolve to loopback are blocked too (best effort: relies on
	// the system resolver knowing "localhost"; it must never succeed).
	if _, err := client.Get(context.Background(), "http://localhost:"+port+"/", nil, 0); err == nil {
		t.Error("localhost must not be reachable with the default policy")
	}

	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Errorf("the internal server received %d request(s)", hits)
	}
}

func TestWebClient_ExplicitlyAllowedNetworkWorks(t *testing.T) {
	var gotUA, gotCustom string
	_, port := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotCustom = r.Header.Get("User-Agent"), r.Header.Get("X-Custom")
		w.Header().Set("X-Reply", "yes")
		fmt.Fprint(w, "hello")
	}))
	client := NewWebClient(WebClientConfig{AllowedNetworks: loopbackOnly})

	hdr := http.Header{}
	hdr.Set("User-Agent", "test-agent")
	hdr.Set("X-Custom", "abc")
	resp, err := client.Get(context.Background(), "http://127.0.0.1:"+port+"/page?q=1#frag", hdr, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || string(resp.Body) != "hello" || resp.Truncated || resp.Header.Get("X-Reply") != "yes" {
		t.Errorf("response = %+v", resp)
	}
	if resp.FinalURL != "http://127.0.0.1:"+port+"/page?q=1" {
		t.Errorf("FinalURL = %q", resp.FinalURL)
	}
	if gotUA != "test-agent" || gotCustom != "abc" {
		t.Errorf("request headers = %q / %q", gotUA, gotCustom)
	}

	// The exception is narrow: another loopback address stays blocked.
	if _, err := client.Get(context.Background(), "http://127.0.0.2:"+port+"/", nil, 0); !errors.Is(err, ErrWebBlocked) {
		t.Errorf("127.0.0.2 error = %v, want ErrWebBlocked", err)
	}
}

func TestWebClient_HostnameResolutionIsChecked(t *testing.T) {
	_, port := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	res := newFakeResolver(map[string][]string{
		"app.test":      {"127.0.0.1"},
		"internal.test": {"10.0.0.5"},
		"mixed.test":    {"8.8.8.8", "127.0.0.1"}, // one good answer must not launder a bad one
		"v6.test":       {"::1"},
	})

	t.Run("allowed by explicit policy", func(t *testing.T) {
		client := NewWebClient(WebClientConfig{Resolver: res, AllowedNetworks: loopbackOnly})
		resp, err := client.Get(context.Background(), "http://app.test:"+port+"/", nil, 0)
		if err != nil || string(resp.Body) != "ok" {
			t.Fatalf("resp=%+v err=%v", resp, err)
		}
		if n := res.lookups("app.test"); n != 1 {
			t.Errorf("host resolved %d times for one connection, want 1 (the validated IP is dialed)", n)
		}
	})

	t.Run("default policy", func(t *testing.T) {
		client := NewWebClient(WebClientConfig{Resolver: res, Timeout: 3 * time.Second})
		for _, host := range []string{"app.test", "internal.test", "mixed.test", "v6.test"} {
			if _, err := client.Get(context.Background(), "http://"+host+":"+port+"/", nil, 0); !errors.Is(err, ErrWebBlocked) {
				t.Errorf("%s error = %v, want ErrWebBlocked", host, err)
			}
		}
	})

	t.Run("unresolvable host", func(t *testing.T) {
		client := NewWebClient(WebClientConfig{Resolver: res})
		_, err := client.Get(context.Background(), "http://nowhere.test/", nil, 0)
		if err == nil || errors.Is(err, ErrWebBlocked) {
			t.Errorf("error = %v, want a DNS failure", err)
		}
	})
}

func TestWebClient_DNSAnswerChangingBetweenRequestsIsRevalidated(t *testing.T) {
	_, port := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	res := newFakeResolver(map[string][]string{"flip.test": {"127.0.0.1"}})
	// Disable connection reuse so every request dials again.
	client := NewWebClient(WebClientConfig{Resolver: res, AllowedNetworks: loopbackOnly})
	client.transport.DisableKeepAlives = true

	if _, err := client.Get(context.Background(), "http://flip.test:"+port+"/", nil, 0); err != nil {
		t.Fatal(err)
	}
	// The name now points somewhere internal: the next dial must refuse it.
	res.mu.Lock()
	res.answers["flip.test"] = []netip.Addr{netip.MustParseAddr("192.168.1.1")}
	res.mu.Unlock()
	if _, err := client.Get(context.Background(), "http://flip.test:"+port+"/", nil, 0); !errors.Is(err, ErrWebBlocked) {
		t.Errorf("rebound name error = %v, want ErrWebBlocked", err)
	}
}

func TestWebClient_RedirectsAreRevalidated(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	var srvURL string
	srv, port := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		switch r.URL.Path {
		case "/to-internal-name":
			http.Redirect(w, r, "http://internal.test:"+r.URL.Query().Get("p")+"/secret", http.StatusFound)
		case "/to-metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		case "/to-loopback-other":
			http.Redirect(w, r, "http://127.0.0.2:"+r.URL.Query().Get("p")+"/", http.StatusFound)
		case "/to-file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case "/to-ftp":
			http.Redirect(w, r, "ftp://example.com/x", http.StatusFound)
		case "/to-credentials":
			http.Redirect(w, r, "http://user:pw@app.test:"+r.URL.Query().Get("p")+"/", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/hop":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			fmt.Fprint(w, "arrived")
		default:
			fmt.Fprint(w, "other")
		}
	}))
	srvURL = srv.URL
	res := newFakeResolver(map[string][]string{"internal.test": {"10.0.0.5"}, "app.test": {"127.0.0.1"}})
	client := NewWebClient(WebClientConfig{Resolver: res, AllowedNetworks: loopbackOnly, MaxRedirects: 3, Timeout: 5 * time.Second})

	blockedTargets := map[string]bool{
		"/to-internal-name?p=" + port:  true,
		"/to-metadata":                 true,
		"/to-loopback-other?p=" + port: true,
	}
	for path := range blockedTargets {
		if _, err := client.Get(context.Background(), srvURL+path, nil, 0); !errors.Is(err, ErrWebBlocked) {
			t.Errorf("redirect via %s error = %v, want ErrWebBlocked", path, err)
		}
	}

	for _, path := range []string{"/to-file", "/to-ftp", "/to-credentials?p=" + port} {
		_, err := client.Get(context.Background(), srvURL+path, nil, 0)
		if err == nil {
			t.Errorf("redirect via %s was followed", path)
		}
	}

	mu.Lock()
	hits["/loop"] = 0
	mu.Unlock()
	if _, err := client.Get(context.Background(), srvURL+"/loop", nil, 0); err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Errorf("redirect loop error = %v", err)
	}
	mu.Lock()
	if hits["/loop"] > 4 { // the first request plus MaxRedirects follow-ups
		t.Errorf("redirect loop hit the server %d times, want at most 4", hits["/loop"])
	}
	mu.Unlock()

	resp, err := client.Get(context.Background(), srvURL+"/hop", nil, 0)
	if err != nil || string(resp.Body) != "arrived" || !strings.HasSuffix(resp.FinalURL, "/final") {
		t.Errorf("a legitimate redirect must work: resp=%+v err=%v", resp, err)
	}

	noRedirects := NewWebClient(WebClientConfig{AllowedNetworks: loopbackOnly, MaxRedirects: -1})
	if _, err := noRedirects.Get(context.Background(), srvURL+"/hop", nil, 0); err == nil {
		t.Error("a negative MaxRedirects must follow none")
	}
}

func TestWebClient_CustomHeadersDoNotCrossOrigins(t *testing.T) {
	type seen struct{ apiKey, auth, ua string }
	var mu sync.Mutex
	var other seen
	_, otherPort := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		other = seen{r.Header.Get("X-Api-Key"), r.Header.Get("Authorization"), r.Header.Get("User-Agent")}
		mu.Unlock()
		fmt.Fprint(w, "other origin")
	}))
	var sameOrigin seen
	srv, _ := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cross":
			http.Redirect(w, r, "http://127.0.0.1:"+otherPort+"/landing", http.StatusFound)
		case "/same":
			http.Redirect(w, r, "/landing", http.StatusFound)
		case "/landing":
			mu.Lock()
			sameOrigin = seen{r.Header.Get("X-Api-Key"), r.Header.Get("Authorization"), r.Header.Get("User-Agent")}
			mu.Unlock()
			fmt.Fprint(w, "same origin")
		}
	}))
	client := NewWebClient(WebClientConfig{AllowedNetworks: loopbackOnly})

	hdr := http.Header{}
	hdr.Set("X-Api-Key", "s3cret")
	hdr.Set("Authorization", "Bearer s3cret")
	hdr.Set("User-Agent", "agent/1")

	if _, err := client.Get(context.Background(), srv.URL+"/cross", hdr, 0); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if other.apiKey != "" || other.auth != "" {
		t.Errorf("secrets leaked to another origin: %+v", other)
	}
	if other.ua != "agent/1" {
		t.Errorf("harmless headers should survive: %+v", other)
	}
	mu.Unlock()

	if _, err := client.Get(context.Background(), srv.URL+"/same", hdr, 0); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if sameOrigin.apiKey != "s3cret" {
		t.Errorf("same-origin redirects keep custom headers: %+v", sameOrigin)
	}
}

func TestWebClient_HonorsContextAndTimeout(t *testing.T) {
	release := make(chan struct{})
	srv, _ := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release) }) // runs before srv.Close

	client := NewWebClient(WebClientConfig{AllowedNetworks: loopbackOnly})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := client.Get(ctx, srv.URL, nil, 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("deadline error = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the context deadline was not enforced")
	}

	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := client.Get(canceled, srv.URL, nil, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled context error = %v", err)
	}

	short := NewWebClient(WebClientConfig{AllowedNetworks: loopbackOnly, Timeout: 100 * time.Millisecond})
	if _, err := short.Get(context.Background(), srv.URL, nil, 0); err == nil {
		t.Error("the client's own Timeout was not enforced")
	}
}

func TestWebClient_BodyAndHeaderLimits(t *testing.T) {
	srv, _ := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bighdr" {
			w.Header().Set("X-Big", strings.Repeat("a", 200_000))
		}
		fmt.Fprint(w, strings.Repeat("x", 10_000))
	}))

	client := NewWebClient(WebClientConfig{AllowedNetworks: loopbackOnly, MaxBodyBytes: 4000, MaxHeaderBytes: 4096})

	resp, err := client.Get(context.Background(), srv.URL+"/", nil, 1000)
	if err != nil || len(resp.Body) != 1000 || !resp.Truncated {
		t.Errorf("per-call limit: len=%d truncated=%v err=%v", len(resp.Body), resp.Truncated, err)
	}
	resp, err = client.Get(context.Background(), srv.URL+"/", nil, 9000)
	if err != nil || len(resp.Body) != 4000 || !resp.Truncated {
		t.Errorf("a call may not exceed the client cap: len=%d truncated=%v err=%v", len(resp.Body), resp.Truncated, err)
	}
	resp, err = client.Get(context.Background(), srv.URL+"/", nil, 0)
	if err != nil || len(resp.Body) != 4000 {
		t.Errorf("default limit: len=%d err=%v", len(resp.Body), err)
	}

	if _, err := client.Get(context.Background(), srv.URL+"/bighdr", nil, 0); err == nil {
		t.Error("oversized response headers must be rejected")
	}

	big := NewWebClient(WebClientConfig{AllowedNetworks: loopbackOnly})
	resp, err = big.Get(context.Background(), srv.URL+"/", nil, 0)
	if err != nil || len(resp.Body) != 10_000 || resp.Truncated {
		t.Errorf("a body under the limit must be complete: len=%d truncated=%v err=%v", len(resp.Body), resp.Truncated, err)
	}
}

func TestParseWebURL(t *testing.T) {
	bad := []string{
		"", "   ", "http://", "https://", "http:///path", "http://%", "http://%zz/", "http://[::1",
		"ftp://example.com/", "file:///etc/passwd", "javascript:alert(1)", "data:text/html,hi", "mailto:a@b.example",
		"//example.com/path", "/relative/path", "example.com", "example.com/path", "localhost:8080",
		"http:example.com", "http://user:pass@example.com/", "http://user@example.com/",
		"http://exa mple.com/", "http://example.com/\nHost: evil", "http://example.com/\x00",
		"http://" + strings.Repeat("a", 3000) + ".example",
	}
	for _, raw := range bad {
		if u, err := ParseWebURL(raw); err == nil {
			t.Errorf("ParseWebURL(%q) accepted: %v", raw, u)
		}
	}

	for _, raw := range []string{"http://example.com", "HTTPS://Example.com:8443/a/b?c=d", "http://[2606:4700::1111]:80/"} {
		if _, err := ParseWebURL(raw); err != nil {
			t.Errorf("ParseWebURL(%q) = %v", raw, err)
		}
	}
	if u, _ := ParseWebURL("https://example.com/p#frag"); u == nil || u.Fragment != "" {
		t.Errorf("fragment should be dropped: %v", u)
	}
}

func TestWebClient_MalformedURLsFailWithoutPanicking(t *testing.T) {
	client := NewWebClient(WebClientConfig{})
	for _, raw := range []string{"", "http://%", "http://", "%%%", "http://[", "\x00", "http://a b"} {
		if _, err := client.Get(context.Background(), raw, nil, 0); err == nil {
			t.Errorf("Get(%q) succeeded", raw)
		}
	}
}

func TestSanitizeRequestHeaders(t *testing.T) {
	got, err := sanitizeRequestHeaders(map[string]string{"x-api-key": "abc", "ACCEPT": "application/json"})
	if err != nil || got.Get("X-Api-Key") != "abc" || got.Get("Accept") != "application/json" {
		t.Errorf("valid headers: %v %v", got, err)
	}

	rejected := []map[string]string{
		{"Host": "internal"},
		{"Connection": "close"},
		{"Transfer-Encoding": "chunked"},
		{"Proxy-Authorization": "x"},
		{"X-Forwarded-For": "127.0.0.1"},
		{"Bad Name": "v"},
		{"Bad:Name": "v"},
		{"": "v"},
		{"X-A": "line\r\nInjected: yes"},
		{"X-A": strings.Repeat("v", 2000)},
	}
	for _, in := range rejected {
		if _, err := sanitizeRequestHeaders(in); err == nil {
			t.Errorf("headers %v accepted", in)
		}
	}

	many := map[string]string{}
	for i := range 20 {
		many[fmt.Sprintf("X-H%d", i)] = "v"
	}
	if _, err := sanitizeRequestHeaders(many); err == nil {
		t.Error("too many headers accepted")
	}
}
