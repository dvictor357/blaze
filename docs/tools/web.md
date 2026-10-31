# Web tools

All web tools receive the invocation context and use a shared `WebClient`. They support only absolute HTTP(S) URLs without embedded credentials. They are fetchers/parsers, not browser automation or a JavaScript renderer.

## Construct once, share the transport

```go
client := tool.NewWebClient(tool.WebClientConfig{})
defer client.Close()
fetch := tool.NewWebFetchTool(client)
read := tool.NewWebReadTool(client)
search := tool.NewWebSearchTool(client)
```

These variables are `adapter.Tool` values for `adapter.NewExecutor`. Passing nil to a tool constructor creates a new secure-default client; explicitly sharing one avoids separate transports.

## Default network policy

- Private, loopback, link-local, unspecified, multicast, and listed special-purpose/reserved addresses are blocked, including IPv4-mapped IPv6 and translation/tunneling ranges.
- DNS is resolved by the client. Checked IP literals are dialed directly, preventing a second unchecked resolution at connect time. Redirect connections use the same checks.
- Ambient `HTTP_PROXY`/`HTTPS_PROXY` settings are ignored.
- Redirects are capped; non-HTTP(S) destinations and URL credentials are rejected. Model-supplied sensitive headers are not retained across origins.
- The request context and a 15s client deadline bound network work. Default response-header cap is 64 KiB and client body cap is 512 KiB; individual tools use smaller body caps.

This policy is defense in depth, not a replacement for a deployment egress firewall or tool authorization. Public servers may still expose sensitive resources; authorize tool use and destinations appropriate to your application. No port allowlist or per-tenant concurrency/rate limit is supplied.

For a trusted integration that deliberately needs a local service, allow only the necessary network prefix:

```go
client := tool.NewWebClient(tool.WebClientConfig{
    AllowedNetworks: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
})
```

This snippet additionally imports `net/netip`. Such an exception grants reachability to that prefix, including arbitrary ports; do not expose this client to untrusted callers without further application policy. Policy is client-local, never a process-global bypass. `WebClientConfig` also accepts a resolver, timeout, redirect cap, body cap, and header cap. A negative redirect cap disables following; zero selects five redirects.

## `web_fetch`

Input is the call's `input` object:

```json
{"url":"https://api.github.com/users/golang","headers":{"Accept":"application/json"}}
```

Raw body is capped at 50 KiB (or a smaller client cap). Output contains HTTP `status`, final `url`, `content_type`, `headers`, `body`, `size`, and `truncated`. HTTP non-2xx statuses are returned as responses; transport/policy errors are tool errors. `Set-Cookie` is not returned. A body exactly at the cap is not marked truncated unless additional bytes exist.

Model-supplied request headers are limited to 16, with bounded name/value lengths. Framing, hop-by-hop, proxy, and forwarding headers are rejected. Do not put secrets in arbitrary URLs or headers; fetched content is returned to the caller/model.

## `web_read`

```json
{"url":"https://go.dev/doc/effective_go"}
```

Reads at most 500 KiB, performs best-effort regex-based main-content extraction and HTML-to-Markdown conversion, and returns metadata, HTTP status, final URL, content, links, and a truncation flag. Markdown is capped at 8 KiB before its truncation notice; links are capped at 20. It does not guarantee removal of all navigation/ads and is not a complete HTML parser. Remote content is untrusted data, not instructions for an agent.

Malformed URLs return errors rather than panicking. Compiled regexes are reused, including during link extraction.

## `web_search`

```json
{"query":"golang context cancellation","max_results":5}
```

Scrapes DuckDuckGo's HTML interface without API keys. Query length is capped at 500 bytes; result count defaults to five and is capped at ten. The response contains `query`, `results` (`title`, `url`, `snippet`), and `count`.

This is best-effort scraping, not a guaranteed search-provider API. Upstream blocking or markup changes can cause errors or empty results. No live search availability guarantee is claimed.
