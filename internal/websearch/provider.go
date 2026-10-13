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
// Providers built via ProviderFor are wrapped in the retry decorator
// (Slice 2b), so SearXNG and Tavily get identical retry semantics: bounded,
// ctx-bound, transient classes only.
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
	// Error semantics (Slice 2a): a typed searchError carrying exactly one
	// class sentinel (ErrAuthConfig / ErrRateLimited / ErrProvider5xx /
	// ErrMalformed / ErrConnection / ErrTimeout), which also wraps the
	// ErrUnavailable umbrella for legacy errors.Is callers; a
	// webfetch.HostNotAllowedError returned as-is (a configuration gap the
	// parent fixes in tools.web_fetch.url_allowlist — never classified,
	// never retried); and (nil, nil) for a valid zero-result response —
	// which is a success, never an error.
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
// Configuration errors (unsupported provider name, missing Tavily key) wrap
// ErrProviderNotConfigured so callers classify them via errors.Is — never
// by message text, so upstream rewording cannot break the mapping; the
// agent maps the sentinel to its "provider not configured" marker.
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
		return &retryingProvider{inner: &searxngProvider{endpoint: pc.Endpoint, opts: Options{
			MaxResults:           pc.MaxResults,
			Timeout:              pc.Timeout,
			HostValidator:        pc.HostValidator,
			AllowPrivateNetworks: pc.AllowPrivateNetworks,
		}}, maxRetries: defaultMaxRetries}, nil
	case "tavily":
		if strings.TrimSpace(pc.TavilyAPIKey) == "" {
			return nil, fmt.Errorf("%w: tavily provider selected but no API key is set (env FAMCLAW_WEB_SEARCH_API_KEY or tools.web_search.tavily_api_key)", ErrProviderNotConfigured)
		}
		return &retryingProvider{inner: NewTavilyProvider(TavilyConfig{APIKey: pc.TavilyAPIKey, Timeout: pc.Timeout}), maxRetries: defaultMaxRetries}, nil
	default:
		return nil, fmt.Errorf("%w: unknown provider %q (supported: searxng [default], tavily)", ErrProviderNotConfigured, pc.Name)
	}
}

// retryingProvider decorates a Provider with bounded, ctx-bound retry of
// transient failures only (web-retrieval plan, Slice 2b). It is a property
// of the injected provider — both SearXNG and Tavily get identical
// semantics — and it adds no global state: the retry budget is a fixed
// constant and the deadline is the caller's ctx (never extended).
//
// Retry policy (exactly the documented transient set, nothing else):
//   - RETRY: ErrRateLimited, ErrProvider5xx, ErrConnection, ErrTimeout.
//     A 429 with a Retry-After header waits that long; any transient class
//     without one gets short capped exponential backoff.
//   - NEVER RETRY: ErrAuthConfig, ErrMalformed, HostNotAllowedError
//     (configuration gaps — they fail identically on retry), the
//     undetermined umbrella, or a valid result.
//   - BOUNDED: at most maxRetries retries after the initial attempt (3
//     attempts total). When the budget runs out, or the remaining ctx time
//     cannot cover the next delay, the LAST error is returned honestly —
//     the model reports "rate-limited", not "unavailable".
//
// The loop always fits inside the caller's ctx deadline: each attempt runs
// under the same ctx (so the provider's own timeout caps each attempt at
// the remaining time), and the pre-sleep check refuses to spend more time
// than the ctx has left.
type retryingProvider struct {
	inner      Provider
	maxRetries int
}

// retryer is implemented by errors that carry a provider Retry-After.
type retryer interface {
	RetryAfter() time.Duration
}

const (
	// defaultMaxRetries is the retry budget: retries AFTER the initial
	// attempt (max 3 attempts total).
	defaultMaxRetries = 2
	// baseRetryDelay is the backoff for a transient failure without a
	// Retry-After; it doubles per attempt, capped at maxRetryDelay.
	baseRetryDelay = 500 * time.Millisecond
	maxRetryDelay  = 2 * time.Second
)

func (r *retryingProvider) Name() string { return r.inner.Name() }

// retryDelay computes the wait before attempt n+1. The exponential schedule
// (base 500 ms, doubling, capped at 2 s) is the floor; a server's Retry-After
// is a minimum, so the effective delay is max(schedule, Retry-After). Waiting
// longer than the server asked is safe (we retry less often); waiting shorter
// would violate its rate-limit window. Negative/zero Retry-After is clamped
// to the schedule.
func retryDelay(err error, attempt int) time.Duration {
	d := baseRetryDelay
	for i := 0; i < attempt; i++ {
		d *= 2
		if d > maxRetryDelay {
			d = maxRetryDelay
			break
		}
	}
	if d > maxRetryDelay {
		d = maxRetryDelay
	}
	if r, ok := err.(retryer); ok {
		if ra := r.RetryAfter(); ra > d {
			return ra
		}
	}
	return d
}

// Search runs the query through the inner provider with bounded retry of
// transient failures (see type doc).
func (r *retryingProvider) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		hits, err := r.inner.Search(ctx, query, limit)
		if err == nil {
			// Success (including a valid zero-result) — never retried.
			return hits, nil
		}
		lastErr = err
		if !isTransient(err) {
			// Auth, malformed, host-not-allowed, undetermined: fail
			// immediately with the honest class.
			return nil, err
		}
		if attempt >= r.maxRetries {
			// Budget exhausted: return the last error class honestly.
			return nil, err
		}
		delay := retryDelay(err, attempt)
		if deadline, ok := ctx.Deadline(); ok {
			// The whole loop must fit inside the caller's ctx: never spend
			// more time than is left (attempts without a deadline are
			// still bounded by the maxRetries budget).
			if time.Until(deadline) <= delay {
				return nil, err
			}
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			// Canceled mid-backoff: return the last error class honestly
			// (the caller's turn is over; a fresh error adds no signal).
			return nil, lastErr
		}
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
