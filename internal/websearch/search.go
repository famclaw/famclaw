package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/famclaw/famclaw/internal/webfetch"
)

const (
	defaultMaxResults = 8
	hardMaxResults    = 16
	defaultTimeout    = 10 * time.Second
)

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

	u := strings.TrimRight(opts.Endpoint, "/") + "/search?q=" + url.QueryEscape(query) + "&format=json"

	res, err := webfetch.Fetch(ctx, u, webfetch.Options{
		MaxBytes:             256 * 1024,
		Timeout:              timeout,
		AllowedTypes:         []string{"application/json"},
		HostValidator:        opts.HostValidator,
		AllowPrivateNetworks: opts.AllowPrivateNetworks,
	})
	if err != nil {
		return nil, fmt.Errorf("web_search: fetch: %w", err)
	}

	var parsed struct {
		Results []Hit `json:"results"`
	}
	if jerr := json.Unmarshal([]byte(res.Text), &parsed); jerr != nil {
		return nil, fmt.Errorf("web_search: decode: %w", jerr)
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
