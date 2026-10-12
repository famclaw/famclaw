package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// tavilyEndpoint is the fixed Tavily search API host. It is a constant, not
// operator config, and deliberately NOT a candidate for
// tools.web_fetch.url_allowlist: that allowlist gates *user-supplied*
// web_fetch targets, whereas this is the operator's *configured* search
// backend selected from tools.web_search.provider. Do not "fix" a missing
// Tavily call by appending api.tavily.com to url_allowlist — that conflates
// the two gates and would imply user fetches could reach the vendor host.
const tavilyEndpoint = "https://api.tavily.com/search"

// tavilyMaxResultsCap mirrors Tavily's own per-request result cap. We request
// min(limit, this) so a misconfigured large limit never yields a provider
// error.
const tavilyMaxResultsCap = 20

// TavilyConfig configures a TavilyProvider.
type TavilyConfig struct {
	// APIKey is the resolved Tavily API key. Never logged, never included
	// in any error message.
	APIKey string
	// Endpoint overrides the fixed API endpoint — test seam for httptest
	// fakes only. Production callers leave it empty.
	Endpoint string
	// Timeout bounds the request (0 → defaultTimeout). Test seam;
	// production callers leave it 0.
	Timeout time.Duration
}

// NewTavilyProvider builds the Tavily hosted search adapter.
func NewTavilyProvider(cfg TavilyConfig) *TavilyProvider {
	ep := cfg.Endpoint
	if ep == "" {
		ep = tavilyEndpoint
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &TavilyProvider{endpoint: ep, key: cfg.APIKey, timeout: timeout}
}

// TavilyProvider implements Provider against the Tavily search API.
//
// Auth (verified against the Tavily API reference, docs.tavily.com, 2026-10):
// the key travels as an "Authorization: Bearer <key>" header on
// POST /search. The legacy body field "api_key" predates this scheme and is
// NOT sent — the header is the documented current contract.
type TavilyProvider struct {
	endpoint string
	key      string
	timeout  time.Duration
}

func (p *TavilyProvider) Name() string { return "tavily" }

type tavilyRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

type tavilyResponse struct {
	Results []Hit `json:"results"`
}

// Search POSTs {query, max_results} to the Tavily API with the Bearer key
// and maps the response's results to the shared Hit shape.
func (p *TavilyProvider) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("web_search: empty query")
	}
	if p.key == "" {
		return nil, fmt.Errorf("%w: tavily provider has no API key configured", ErrUnavailable)
	}
	n := limit
	if n <= 0 {
		n = defaultMaxResults
	}
	if n > hardMaxResults {
		n = hardMaxResults
	}
	if n > tavilyMaxResultsCap {
		n = tavilyMaxResultsCap
	}

	body, err := json.Marshal(tavilyRequest{Query: query, MaxResults: n})
	if err != nil {
		return nil, fmt.Errorf("web_search: encode tavily request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("web_search: build tavily request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.key)

	// Guarded transport, webfetch-style: direct connection only (Proxy nil
	// so no HTTP(S)_PROXY env var can reroute the request), a hard client
	// timeout as the deadline backstop, no proxy, and response reads are
	// byte-capped. The vendor host comes from config/constant — not from
	// user input — so the url_allowlist HostValidator is intentionally NOT
	// applied here (see tavilyEndpoint comment).
	client := &http.Client{
		Timeout:   p.timeout,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to disallowed scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: tavily: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil {
		return nil, fmt.Errorf("%w: tavily: read response: %v", ErrUnavailable, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Never echo the body: on auth failures Tavily's response can
		// include key-derived details, and the body is not worth leaking
		// into the tool result the LLM would relay.
		return nil, fmt.Errorf("%w: tavily: non-2xx status %d", ErrUnavailable, resp.StatusCode)
	}
	if len(raw) > 1024*1024 {
		return nil, fmt.Errorf("%w: tavily: response exceeds 1 MiB cap", ErrUnavailable)
	}

	var parsed tavilyResponse
	if jerr := json.Unmarshal(raw, &parsed); jerr != nil {
		return nil, fmt.Errorf("%w: tavily: malformed JSON response: %v", ErrUnavailable, jerr)
	}
	if len(parsed.Results) > n {
		parsed.Results = parsed.Results[:n]
	}
	return parsed.Results, nil
}
