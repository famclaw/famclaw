package websearch

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Provider is the injected search-backend boundary for the web_search tool.
// Implementations map their native request/response to the shared Hit shape.
// No global state: providers are constructed with their full configuration
// and passed in (repository rule 4/7 — no package-level mutable provider).
//
// Adding a hosted backend (e.g. "exa", "brave") later is a config addition
// plus one more case in ProviderFor, not a rework of the search tool
// contract.
type Provider interface {
	// Name returns a stable identifier for logs and config ("searxng",
	// "tavily").
	Name() string
	// Search runs a single query and returns up to limit hits.
	//
	// Error semantics match websearch.Search: ErrUnavailable (wrapped with a
	// detail) for unreachable/non-searchable backends, a
	// webfetch.HostNotAllowedError returned as-is (a configuration gap the
	// parent fixes in tools.web_fetch.url_allowlist), and (nil, nil) for a
	// valid zero-result response — which is a success, never an error.
	Search(ctx context.Context, query string, limit int) ([]Hit, error)
}

// ProviderConfig carries everything ProviderFor needs to build a Provider.
// It is a plain value assembled by the caller from the resolved process
// config — it does not import the config package, which keeps the websearch
// package free of a config dependency (no import cycle with the agent).
type ProviderConfig struct {
	// Name selects the backend: "" (unset), "searxng" (default) or "tavily".
	// Any other value is a configuration error.
	Name string
	// Endpoint is the SearXNG (or compatible) JSON endpoint; required for
	// the searxng provider.
	Endpoint string
	// MaxResults caps results per query (0 → defaultMaxResults).
	MaxResults int
	// Timeout bounds a single search (0 → defaultTimeout).
	Timeout time.Duration
	// HostValidator gates the search endpoint host for the searxng
	// provider (the agent passes the web_fetch url_allowlist gate). The
	// hosted providers do not use it — see TavilyProvider for why.
	HostValidator func(host string) error
	// AllowPrivateNetworks relaxes the DNS-time private-IP guard for the
	// searxng endpoint (self-hosted SearXNG commonly runs on the home LAN;
	// the operator opts in via tools.web_fetch.block_private_networks).
	AllowPrivateNetworks bool
	// TavilyAPIKey is the resolved API key for the tavily provider (env
	// FAMCLAW_WEB_SEARCH_API_KEY over YAML tavily_api_key — see
	// config.resolveWebSearchAPIKey). Never logged; never echoed in errors.
	TavilyAPIKey string
}

// ProviderFor builds the configured search provider. An empty Name selects
// the SearXNG default, so an existing deployment that sets only
// tools.web_search.endpoint behaves byte-identically to before this
// boundary existed.
//
// Config shapes for the operator options guide (all config additions — no
// code rework, per the accepted web-retrieval plan §5):
//
//   - A (SearXNG floor, DEFAULT):    provider: searxng (or unset) +
//     endpoint: http://<lan-host>:8888 + the host in web_fetch.url_allowlist.
//   - B (Tavily, hosted):            provider: tavily + key via
//     FAMCLAW_WEB_SEARCH_API_KEY (or tavily_api_key in YAML).
//   - C (Exa, hosted, later):        provider: exa + exa key — new adapter,
//     same interface.
//   - D (Brave, hosted, later):      provider: brave + brave key — new
//     adapter, same interface; fetch stays local.
//   - E (Firecrawl extract, later):  a web_fetch extraction backend, a
//     separate surface from this search interface (deferred).
func ProviderFor(pc ProviderConfig) (Provider, error) {
	name := strings.ToLower(strings.TrimSpace(pc.Name))
	switch name {
	case "", "searxng":
		return &searxngProvider{endpoint: pc.Endpoint, opts: Options{
			MaxResults:           pc.MaxResults,
			Timeout:              pc.Timeout,
			HostValidator:        pc.HostValidator,
			AllowPrivateNetworks: pc.AllowPrivateNetworks,
		}}, nil
	case "tavily":
		if strings.TrimSpace(pc.TavilyAPIKey) == "" {
			return nil, fmt.Errorf("web_search: tavily provider selected but no API key is set (env FAMCLAW_WEB_SEARCH_API_KEY or tools.web_search.tavily_api_key)")
		}
		return NewTavilyProvider(TavilyConfig{APIKey: pc.TavilyAPIKey}), nil
	default:
		return nil, fmt.Errorf("web_search: unknown provider %q (supported: searxng [default], tavily)", pc.Name)
	}
}

// searxngProvider is the SearXNG (or compatible JSON) implementation: a thin
// wrapper around the existing Search function so callers, tests, the
// host-allowlist gate, and the startup probe all keep their current
// behavior byte-for-byte.
type searxngProvider struct {
	endpoint string
	opts     Options
}

func (p *searxngProvider) Name() string { return "searxng" }

func (p *searxngProvider) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	o := p.opts
	o.MaxResults = limit
	return Search(ctx, query, o)
}
