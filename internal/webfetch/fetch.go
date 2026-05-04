package webfetch

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"time"
)

const defaultMaxBytes = 256 * 1024
const defaultTimeout = 15 * time.Second
const defaultUserAgent = "famclaw-webfetch/1 (+https://github.com/famclaw/famclaw)"

// defaultAllowedTypes returns a fresh slice each call so callers cannot
// mutate a shared package-level default. Project policy: no global state.
func defaultAllowedTypes() []string {
	return []string{"text/html", "text/plain", "application/json", "application/xhtml+xml"}
}

// Options controls a single Fetch call.
type Options struct {
	MaxBytes     int64
	Timeout      time.Duration
	AllowedTypes []string
	UserAgent    string

	// HostValidator, if non-nil, is invoked for the initial URL host AND
	// every redirect target host. Returning a non-nil error aborts the
	// fetch. Callers use this to enforce per-host allowlists across
	// redirects (preventing an allowed host from 302-ing to a disallowed
	// one).
	HostValidator func(host string) error

	// AllowPrivateNetworks disables the default DNS-resolution-time IP
	// guard that rejects loopback, private (RFC1918/ULA), link-local,
	// multicast, and unspecified addresses. Test-only seam — production
	// callers must leave this false to prevent SSRF into the home LAN.
	AllowPrivateNetworks bool
}

// Result carries the fetch outcome.
type Result struct {
	URL         string `json:"url"`
	StatusCode  int    `json:"status_code"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
	Truncated   bool   `json:"truncated"`
	Text        string `json:"text"`
}

// Fetch retrieves rawURL with the given options. Initial scheme is checked
// (http/https only); HostValidator is applied to the initial host and to
// every redirect target; the dialer rejects private/loopback/link-local IPs
// unless opts.AllowPrivateNetworks is set.
func Fetch(ctx context.Context, rawURL string, opts Options) (*Result, error) {
	if opts.MaxBytes < 0 {
		return nil, fmt.Errorf("web fetch: max_bytes must be >= 0 (got %d)", opts.MaxBytes)
	}
	if opts.Timeout < 0 {
		return nil, fmt.Errorf("web fetch: timeout must be >= 0 (got %s)", opts.Timeout)
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = defaultMaxBytes
	}
	if opts.Timeout == 0 {
		opts.Timeout = defaultTimeout
	}
	if len(opts.AllowedTypes) == 0 {
		opts.AllowedTypes = defaultAllowedTypes()
	}
	if opts.UserAgent == "" {
		opts.UserAgent = defaultUserAgent
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("web fetch: parse url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("web fetch: scheme %q not allowed", parsed.Scheme)
	}
	if opts.HostValidator != nil {
		if err := opts.HostValidator(parsed.Hostname()); err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("web fetch: build request: %w", err)
	}
	req.Header.Set("User-Agent", opts.UserAgent)

	// Proxy is intentionally nil. http.ProxyFromEnvironment would let
	// HTTP(S)_PROXY env vars route the connection through an external
	// proxy, which would bypass safeDialContext's private-IP / DNS
	// rebinding guard — the proxy, not our dialer, would decide where
	// the connection actually terminates. Direct connections only.
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           safeDialContext(opts.AllowPrivateNetworks),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   opts.Timeout,
		ExpectContinueTimeout: 1 * time.Second,
	}

	client := &http.Client{
		Timeout:   opts.Timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to disallowed scheme %q", req.URL.Scheme)
			}
			if opts.HostValidator != nil {
				if err := opts.HostValidator(req.URL.Hostname()); err != nil {
					return fmt.Errorf("redirect blocked: %w", err)
				}
			}
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web fetch: do: %w", err)
	}
	defer resp.Body.Close()

	// resp.Request.URL is the final URL after redirects — accurate for
	// audit trails. Fall back to rawURL if the transport didn't populate
	// it (defensive — current Go always sets it for client.Do results).
	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	res := &Result{
		URL:         finalURL,
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
	}

	mediaType, _, mtErr := mime.ParseMediaType(res.ContentType)
	if mtErr != nil {
		mediaType = res.ContentType
	}

	allowed := false
	for _, t := range opts.AllowedTypes {
		if t == mediaType {
			allowed = true
			break
		}
	}
	if !allowed {
		return res, fmt.Errorf("web fetch: content type %q not allowed", mediaType)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return res, fmt.Errorf("web fetch: non-2xx status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, opts.MaxBytes+1))
	if err != nil {
		return res, fmt.Errorf("web fetch: read body: %w", err)
	}

	if int64(len(body)) > opts.MaxBytes {
		res.Truncated = true
		body = body[:opts.MaxBytes]
	}
	res.Bytes = int64(len(body))

	switch mediaType {
	case "text/html", "application/xhtml+xml":
		text, err := ExtractText(body)
		if err != nil {
			return res, fmt.Errorf("web fetch: extract: %w", err)
		}
		res.Text = text
	default:
		res.Text = string(body)
	}

	return res, nil
}

// safeDialContext returns a DialContext that rejects private, loopback,
// link-local, multicast, and unspecified IPs unless allowPrivate is true.
// Defends against SSRF into the home LAN where routers, NAS, Home
// Assistant, and other services run on private ranges.
func safeDialContext(allowPrivate bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if allowPrivate {
		return dialer.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("web fetch: split host:port %q: %w", addr, err)
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("web fetch: resolve %q: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("web fetch: no addresses resolved for host %q", host)
		}
		// Vet every resolved IP. If any one is in a blocked range, refuse
		// the entire request (fail-closed: a host that resolves to both
		// public and LAN addresses must not be reachable, since DNS
		// could shift to the LAN entry between connections).
		vetted := make([]net.IP, 0, len(ips))
		for _, ipa := range ips {
			if blockedIP(ipa.IP) {
				return nil, fmt.Errorf("web fetch: blocked IP %s for host %q", ipa.IP, host)
			}
			vetted = append(vetted, ipa.IP)
		}
		// Try each vetted IP in turn so the standard A/AAAA fallback
		// works when the first address is unreachable. Dial the IP
		// directly (not the hostname) so the connection target is the
		// one we already vetted — avoids a DNS-rebinding race.
		var lastErr error
		for _, ip := range vetted {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, fmt.Errorf("web fetch: dial %q: %w", host, lastErr)
	}
}

// blockedIP reports whether the given IP falls in a range web_fetch must
// not reach: loopback, private (RFC1918 / RFC4193 ULA), link-local,
// multicast, or unspecified.
func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}
