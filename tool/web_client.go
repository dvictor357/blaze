package tool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// ErrWebBlocked is returned (wrapped) when a request, a DNS answer or a
// redirect targets an address the client policy does not allow.
var ErrWebBlocked = errors.New("destination is not allowed by the web client policy")

// WebResolver resolves host names. *net.Resolver implements it.
type WebResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// WebClientConfig configures a WebClient. Zero values select safe defaults.
type WebClientConfig struct {
	// Timeout bounds one Get including redirects and body read (default 15s).
	// The caller's context deadline applies too, whichever is sooner.
	Timeout time.Duration
	// MaxRedirects is the number of redirects followed (0 = default 5,
	// negative = follow none).
	MaxRedirects int
	// MaxBodyBytes caps any response body (default 512 KiB). Get may ask for
	// less, never more.
	MaxBodyBytes int64
	// MaxHeaderBytes caps response headers (default 64 KiB).
	MaxHeaderBytes int64
	// AllowedNetworks are explicit exceptions to the default deny of private,
	// loopback, link-local, multicast, unspecified and reserved addresses:
	// an address inside one of these prefixes may be dialed. It is how tests
	// and trusted deployments reach local services (for example
	// 127.0.0.1/32), one narrow prefix at a time. There is no global switch.
	AllowedNetworks []netip.Prefix
	// Resolver resolves host names (default net.DefaultResolver).
	Resolver WebResolver
}

// WebClient is an HTTP GET client for tools that fetch URLs chosen by a
// model, hardened against SSRF:
//
//   - only http and https URLs without credentials are accepted;
//   - the host is resolved by the client itself, every resolved address is
//     checked, and the connection is made to the validated IP, so DNS cannot
//     change the answer between check and connect (DNS rebinding);
//   - the same check runs for every redirect hop, and redirects are capped;
//   - ambient proxy settings are ignored, so a proxy cannot bypass the check;
//   - caller-supplied headers are dropped when a redirect leaves the origin;
//   - response headers and bodies are size-limited and the context is honored.
//
// A WebClient reuses connections and is safe for concurrent use.
type WebClient struct {
	cfg          WebClientConfig
	maxRedirects int
	allowed      []netip.Prefix
	transport    *http.Transport
	client       *http.Client
}

// NewWebClient creates a WebClient with the given policy.
func NewWebClient(cfg WebClientConfig) *WebClient {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 512 << 10
	}
	if cfg.MaxHeaderBytes <= 0 {
		cfg.MaxHeaderBytes = 64 << 10
	}
	if cfg.Resolver == nil {
		cfg.Resolver = net.DefaultResolver
	}
	c := &WebClient{cfg: cfg, maxRedirects: cfg.MaxRedirects}
	switch {
	case cfg.MaxRedirects == 0:
		c.maxRedirects = 5
	case cfg.MaxRedirects < 0:
		c.maxRedirects = 0
	}
	for _, p := range cfg.AllowedNetworks {
		if p.IsValid() {
			c.allowed = append(c.allowed, p.Masked())
		}
	}

	c.transport = &http.Transport{
		Proxy:                  nil, // never honor HTTP(S)_PROXY: it would bypass dial-time checks
		DialContext:            c.dial,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  cfg.Timeout,
		MaxResponseHeaderBytes: cfg.MaxHeaderBytes,
		MaxIdleConns:           16,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        30 * time.Second,
	}
	c.client = &http.Client{Transport: c.transport, CheckRedirect: c.checkRedirect}
	return c
}

// webClientOrDefault returns client, or a new client with the default policy.
func webClientOrDefault(client *WebClient) *WebClient {
	if client != nil {
		return client
	}
	return NewWebClient(WebClientConfig{})
}

// Close releases idle connections.
func (c *WebClient) Close() { c.transport.CloseIdleConnections() }

// WebResponse is a fetched response.
type WebResponse struct {
	Status    int
	Header    http.Header
	FinalURL  string
	Body      []byte
	Truncated bool
}

// Get fetches rawURL with the given extra request headers. At most maxBody
// bytes of the body are returned (0 = the client's MaxBodyBytes) and
// Truncated reports whether more was available. Any HTTP status is returned
// as a response, not an error.
func (c *WebClient) Get(ctx context.Context, rawURL string, header http.Header, maxBody int64) (*WebResponse, error) {
	u, err := ParseWebURL(rawURL)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	for k, vv := range header {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the echoed URL; keep the cause for errors.Is
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	limit := c.cfg.MaxBodyBytes
	if maxBody > 0 && maxBody < limit {
		limit = maxBody
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	truncated := int64(len(body)) > limit
	if truncated {
		body = body[:limit]
	}
	return &WebResponse{
		Status:    resp.StatusCode,
		Header:    resp.Header.Clone(),
		FinalURL:  resp.Request.URL.String(),
		Body:      body,
		Truncated: truncated,
	}, nil
}

// ParseWebURL parses and validates a URL for fetching: it must be absolute
// http or https with a host, no credentials and a reasonable length. Anything
// else, including scheme-less text, is an error. It never panics.
func ParseWebURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("url is required")
	}
	if len(raw) > 2048 {
		return nil, errors.New("url is too long")
	}
	for _, r := range raw {
		if unicode.IsControl(r) || r == ' ' {
			return nil, errors.New("invalid url: contains whitespace or control characters")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if err := checkWebURL(u); err != nil {
		return nil, err
	}
	u.Fragment = ""
	return u, nil
}

func checkWebURL(u *url.URL) error {
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return errors.New("invalid url: only absolute http:// and https:// URLs are supported")
	}
	if u.Opaque != "" || u.Hostname() == "" {
		return errors.New("invalid url: missing host")
	}
	if u.User != nil {
		return errors.New("invalid url: credentials in the URL are not allowed")
	}
	return nil
}

// checkRedirect revalidates every hop (the dial check covers the address).
func (c *WebClient) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > c.maxRedirects {
		return fmt.Errorf("stopped after %d redirects", c.maxRedirects)
	}
	if err := checkWebURL(req.URL); err != nil {
		return fmt.Errorf("redirect rejected: %w", err)
	}
	if !sameOrigin(via[0].URL, req.URL) {
		// Custom (possibly secret) headers must not follow a redirect to
		// another origin. Go already strips Authorization and Cookie for
		// other domains; this covers every other caller-chosen header.
		for k := range req.Header {
			if !redirectSafeHeaders[k] {
				delete(req.Header, k)
			}
		}
	}
	return nil
}

var redirectSafeHeaders = map[string]bool{
	"User-Agent":      true,
	"Accept":          true,
	"Accept-Language": true,
	"Accept-Encoding": true,
	"Referer":         true,
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// dial resolves host, rejects the whole host if any answer is not allowed,
// and connects to a validated IP literal.
func (c *WebClient) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	var addrs []netip.Addr
	if ip, perr := netip.ParseAddr(host); perr == nil {
		addrs = []netip.Addr{ip}
	} else {
		addrs, err = c.cfg.Resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("dns lookup failed: %w", err)
		}
		if len(addrs) == 0 {
			return nil, errors.New("dns lookup returned no addresses")
		}
		if len(addrs) > 8 {
			addrs = addrs[:8]
		}
	}

	for i, a := range addrs {
		a = a.Unmap().WithZone("")
		if !c.addrAllowed(a) {
			return nil, ErrWebBlocked
		}
		addrs[i] = a
	}

	d := net.Dialer{Timeout: 10 * time.Second}
	var lastErr error
	for _, a := range addrs {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

func (c *WebClient) addrAllowed(a netip.Addr) bool {
	for _, p := range c.allowed {
		if p.Contains(a) {
			return true
		}
	}
	return !blockedWebAddr(a)
}

// reservedPrefixes are special-purpose ranges that are not covered by the
// netip classification helpers. NAT64, 6to4 and Teredo are blocked outright
// because they can embed arbitrary IPv4 destinations.
var reservedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // "this network"
		"100.64.0.0/10",   // carrier-grade NAT
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved + limited broadcast
		"::/96",           // IPv4-compatible (deprecated)
		"64:ff9b::/96",    // NAT64
		"64:ff9b:1::/48",  // local-use NAT64
		"100::/64",        // discard-only
		"2001::/32",       // Teredo
		"2001:db8::/32",   // documentation
		"2002::/16",       // 6to4
		"fec0::/10",       // deprecated site-local
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// blockedWebAddr reports whether a is private, loopback, link-local,
// multicast, unspecified or otherwise reserved. a must be unmapped.
func blockedWebAddr(a netip.Addr) bool {
	if !a.IsValid() || a.IsUnspecified() || a.IsLoopback() || a.IsPrivate() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() {
		return true
	}
	for _, p := range reservedPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// sanitizeRequestHeaders validates model-supplied request headers: bounded
// count and size, token names, no control characters, and no hop-by-hop or
// connection-framing headers.
func sanitizeRequestHeaders(in map[string]string) (http.Header, error) {
	if len(in) > 16 {
		return nil, errors.New("too many headers (max 16)")
	}
	out := make(http.Header, len(in))
	for k, v := range in {
		if k == "" || len(k) > 64 || len(v) > 1024 {
			return nil, fmt.Errorf("invalid header %q", k)
		}
		for _, r := range k {
			if !(r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
				return nil, fmt.Errorf("invalid header name %q", k)
			}
		}
		for _, r := range v {
			if r < 0x20 && r != '\t' || r == 0x7f {
				return nil, fmt.Errorf("invalid value for header %q", k)
			}
		}
		canon := http.CanonicalHeaderKey(k)
		if forbiddenRequestHeaders[canon] || strings.HasPrefix(canon, "Proxy-") {
			return nil, fmt.Errorf("header %q is not allowed", k)
		}
		out.Set(canon, v)
	}
	return out, nil
}

var forbiddenRequestHeaders = map[string]bool{
	"Host":              true,
	"Connection":        true,
	"Content-Length":    true,
	"Transfer-Encoding": true,
	"Upgrade":           true,
	"Te":                true,
	"Trailer":           true,
	"Expect":            true,
	"Keep-Alive":        true,
	"X-Forwarded-For":   true,
	"X-Forwarded-Host":  true,
	"X-Forwarded-Proto": true,
	"Forwarded":         true,
	"X-Real-Ip":         true,
	"Accept-Encoding":   true,
}
