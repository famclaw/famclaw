package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// ErrUnavailable is returned when the search backend cannot be reached or
// returned a non-searchable response. Callers use errors.Is to detect it and
// translate it into an honest "I could not search right now" reply rather
// than letting the LLM hallucinate results. It is distinct from a zero-result
// response, which is a successful search that simply found nothing.
var ErrUnavailable = errors.New("web_search: search backend unavailable")

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
// Error semantics:
//   - ErrUnavailable: the backend could not be reached or returned a
//     non-searchable response (connection refused, timeout, wrong content
//     type, non-2xx, etc.). The caller MUST report this to the user honestly
//     rather than inventing results.
//   - A hostNotAllowedError from the URL allowlist is returned as-is — it is
//     a configuration gap the parent fixes, not a transient failure.
//   - A zero-length hit slice with a nil error means the backend responded
//     normally but found no matches. This is NOT an error and must not be
//     confused with ErrUnavailable.
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
	endpointParsed.Path = path.Join(endpointParsed.Path, "search")
	if !strings.HasPrefix(endpointParsed.Path, "/") {
		endpointParsed.Path = "/" + endpointParsed.Path
	}
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
		// configuration error, not a backend-unavailability.
		var hostErr *webfetch.HostNotAllowedError
		if errors.As(err, &hostErr) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	var parsed struct {
		Results []Hit `json:"results"`
	}
	if jerr := json.Unmarshal([]byte(res.Text), &parsed); jerr != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, jerr)
	}
	if len(parsed.Results) > n {
		parsed.Results = parsed.Results[:n]
	}
	return parsed.Results, nil
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
		if u := strings.TrimSpace(h.URL); u != "" {
			b.WriteString("\n   ")
			b.WriteString(u)
		}
		if c := strings.TrimSpace(h.Content); c != "" {
			b.WriteString("\n   ")
			b.WriteString(c)
		}
	}
	return b.String()
}
