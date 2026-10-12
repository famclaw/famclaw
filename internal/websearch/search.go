package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/famclaw/famclaw/internal/webfetch"
)

const (
	defaultMaxResults = 8
	hardMaxResults    = 16
	defaultTimeout    = 30 * time.Second
)

// SanitizeEndpoint parses rawEndpoint and returns a string with any
// embedded userinfo (username/password) stripped, so it is safe to log.
// A URL like http://user:pass@host:8888/search becomes
// http://host:8888/search. If the URL cannot be parsed, a placeholder
// is returned (never the raw input, which may contain credentials).
func SanitizeEndpoint(rawEndpoint string) string {
	u, err := url.Parse(rawEndpoint)
	if err != nil {
		return "[unparseable endpoint]"
	}
	u.User = nil
	return u.String()
}

// JoinSearchPath appends the "search" path segment to base, avoiding a
// double-append when the configured endpoint already ends with the search
// path. This handles all endpoint shapes:
//   - bare host ("")      → "/search"
//   - base path ("/searx")  → "/searx/search"
//   - full URL ("/searx/search") → "/searx/search" (unchanged)
//   - trailing slash ("/searx/") → "/searx/search"
//
// Both websearch.Search and cmd/famclaw.checkSearchEndpointReachable use
// this helper so the URL construction cannot diverge.
func JoinSearchPath(base string) string {
	base = strings.TrimRight(base, "/")
	if base == "search" || strings.HasSuffix(base, "/search") {
		return base
	}
	joined := path.Join(base, "search")
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	return joined
}

// Options configures a Search call.
type Options struct {
	Endpoint             string
	MaxResults           int
	Timeout              time.Duration
	HostValidator        func(host string) error
	AllowPrivateNetworks bool
}

// Hit is one search result.
type Hit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// Search runs query against the configured SearXNG endpoint and returns
// up to opts.MaxResults hits.
//
// Error semantics (web-retrieval plan, Slice 2a — typed classes, all of
// which also wrap the ErrUnavailable umbrella):
//   - ErrAuthConfig: the endpoint answered 401/403 (an auth/config gap).
//   - ErrRateLimited: 429 (upstream bot-blocking is common on SearXNG's
//     engines) — the retry decorator may retry it.
//   - ErrProvider5xx: 5xx from the endpoint.
//   - ErrMalformed: 2xx but not JSON (e.g. SearXNG without format=json
//     enabled — HTML error page), or unparseable JSON.
//   - ErrConnection / ErrTimeout: transport-level failures.
//   - ErrUnavailable: any other non-2xx status (400/404/...) — a
//     configuration/misuse the operator fixes, not transient.
//   - A hostNotAllowedError from the URL allowlist is returned as-is — it
//     is a configuration gap the parent fixes, not a transient failure,
//     and is never classified or retried.
//   - A zero-length hit slice with a nil error means the backend responded
//     normally but found no matches. This is NOT an error and must not be
//     confused with ErrUnavailable.
//
// None of these sentinels is ever returned for a genuine empty result:
// report honestly, never invent or mask.
func Search(ctx context.Context, query string, opts Options) ([]Hit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("web_search: empty query")
	}
	if opts.Endpoint == "" {
		return nil, fmt.Errorf("web_search: endpoint not configured")
	}
	n := opts.MaxResults
	if n <= 0 {
		n = defaultMaxResults
	}
	if n > hardMaxResults {
		n = hardMaxResults
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	// Reject whitespace-only endpoints — url.Parse(" ") yields a URL with
	// no host, and path.Join("", "search") would silently produce
	// "/search" (a relative path) instead of a clear configuration error.
	endpointParsed, perr := url.Parse(strings.TrimSpace(opts.Endpoint))
	if perr != nil {
		return nil, fmt.Errorf("web_search: parse endpoint: %w", perr)
	}
	if endpointParsed.Host == "" {
		return nil, fmt.Errorf("web_search: endpoint %q must include a host", opts.Endpoint)
	}
	endpointParsed.Path = JoinSearchPath(endpointParsed.Path)
	q := endpointParsed.Query()
	q.Set("q", query)
	q.Set("format", "json")
	endpointParsed.RawQuery = q.Encode()
	u := endpointParsed.String()

	// Pre-check the endpoint host with the HostValidator so we can
	// distinguish "host not allowed" (a configuration error the parent
	// fixes in url_allowlist) from "backend unreachable" (a transient
	// failure the model must report honestly). webfetch.Fetch re-runs the
	// same validator on redirect targets.
	if opts.HostValidator != nil {
		if err := opts.HostValidator(endpointParsed.Hostname()); err != nil {
			return nil, err
		}
	}

	res, err := webfetch.Fetch(ctx, u, webfetch.Options{
		MaxBytes:             256 * 1024,
		Timeout:              timeout,
		AllowedTypes:         []string{"application/json"},
		HostValidator:        opts.HostValidator,
		AllowPrivateNetworks: opts.AllowPrivateNetworks,
	})
	if err != nil {
		// A redirect-bounce through an allowlist violation is still a
		// configuration error, not a backend-unavailability: return it
		// as-is (never classified, never retried).
		var hostErr *webfetch.HostNotAllowedError
		if errors.As(err, &hostErr) {
			return nil, err
		}
		// Classify the failure (Slice 2a): webfetch returns the Result even
		// on non-2xx, so the real HTTP status is available for 4xx/5xx
		// mapping; without a Result the failure is transport-level.
		return nil, classifyFetchError(res, err)
	}

	var parsed struct {
		Results []Hit `json:"results"`
	}
	if jerr := json.Unmarshal([]byte(res.Text), &parsed); jerr != nil {
		// 2xx JSON content type but unparseable body: the provider answered
		// but the payload is not a search response (malformed JSON).
		return nil, newClassified(ErrMalformed, fmt.Sprintf("malformed JSON response: %v", jerr), 0)
	}
	if len(parsed.Results) > n {
		parsed.Results = parsed.Results[:n]
	}
	return parsed.Results, nil
}

// classifyFetchError maps a webfetch.Fetch failure to exactly one class
// sentinel (Slice 2a). webfetch returns the Result alongside the error for
// HTTP-level failures, so the real status can be classified:
//
//   - 401/403 → ErrAuthConfig, 429 → ErrRateLimited, 5xx → ErrProvider5xx.
//   - 2xx with a non-JSON content type → ErrMalformed (SearXNG without
//     format=json enabled returns HTML — the F4 misconfig, now surfaced
//     with an actionable detail instead of a generic "unavailable").
//   - 2xx with a JSON content type → the body read failed mid-stream:
//     ErrConnection.
//   - any other non-2xx (400/404/405/...) → the ErrUnavailable umbrella
//     with a status detail: configuration/misuse, never retried.
//   - no Result at all (connection refused, DNS, timeout, redirect guard)
//     → classifyTransport: ErrTimeout or ErrConnection.
func classifyFetchError(res *webfetch.Result, err error) error {
	if res != nil && res.StatusCode != 0 {
		switch {
		case res.StatusCode >= 200 && res.StatusCode < 300:
			// The listener answered 2xx. The failure is the content type
			// (or a mid-stream read error). Check the negotiated type, not
			// the error text: a JSON-typed 2xx can only have failed on the
			// body read, anything else is a non-JSON answer.
			mediaType, _, _ := mime.ParseMediaType(res.ContentType)
			if mediaType == "application/json" {
				return newClassified(ErrConnection, "read response: "+err.Error(), 0)
			}
			return newClassified(ErrMalformed,
				fmt.Sprintf("endpoint returned %q, not application/json (is SearXNG's format=json enabled?)", res.ContentType), 0)
		default:
			return classifyHTTP(res.StatusCode, 0)
		}
	}
	return classifyTransport(err)
}

// FormatHits renders hits as a compact text block for the LLM tool reply.
func FormatHits(hits []Hit) string {
	if len(hits) == 0 {
		return "no results"
	}
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%d. %s", i+1, strings.TrimSpace(h.Title))
		// Guarantee a source URL on every hit: SearXNG occasionally returns
		// a result with an empty url. Render an explicit placeholder instead
		// of dropping the line — a missing URL must never tempt the model
		// into inventing one (web-retrieval plan, Slice 4: behavioral guard
		// only, no schema change).
		if u := strings.TrimSpace(h.URL); u != "" {
			b.WriteString("\n   ")
			b.WriteString(u)
		} else {
			b.WriteString("\n   (source URL not provided by provider)")
		}
		if c := strings.TrimSpace(h.Content); c != "" {
			b.WriteString("\n   ")
			b.WriteString(c)
		}
	}
	return b.String()
}
