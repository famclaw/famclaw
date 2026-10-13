package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// probeBodyCap bounds how much of the probe response is read into memory. A
// real JSON search response is small; a hostile or misconfigured endpoint
// must not be able to make the startup probe buffer an unbounded body.
const probeBodyCap = 1 << 20 // 1 MiB

// probeQuery is a marker query no real user would search. It exists so the
// startup diagnostic is recognizably a famclaw probe in server logs.
const probeQuery = "__famclaw_startup_probe__"

// defaultProbeTimeout applies when the caller passes a non-positive timeout: a zero value must not mean "time out immediately".
const defaultProbeTimeout = 10 * time.Second

// ProbeSearchEndpoint performs a REAL startup probe against the configured
// search endpoint: it issues an actual JSON search request (format=json) and
// verifies the response is a usable JSON search payload — not merely "any
// HTTP status." This is the honest replacement for the old any-status
// reachability check, which passed a SearXNG answering 200 HTML (e.g.
// format=json disabled) and only surfaced the misconfiguration at first use
// as a generic "unavailable."
//
// The probe is best-effort and does NOT gate startup. The returned error
// carries an actionable, class-specific message:
//
//	2xx + JSON object with a "results" field → nil (reachable)
//	2xx + non-JSON body            → actionable: is format=json enabled? (F4)
//	401/403                        → auth or configuration problem
//	429                            → rate limited (upstream bot block is common)
//	5xx                            → provider server error
//	other non-2xx                  → configuration or misuse
//	connection failure             → timeout (slow, not necessarily down) vs unreachable
//
// The returned errors are classified (ErrMalformed / ErrAuthConfig /
// ErrRateLimited / ErrProvider5xx / ErrTimeout / ErrConnection / the
// ErrUnavailable residual) so any future caller can branch on the class.
//
// Credentials embedded in the endpoint (http://user:pass@host) are stripped
// before the request is built: this is a diagnostic — the probe only checks
// that the host is listening and speaking the search protocol, and it must
// never transmit embedded userinfo to the backend.
func ProbeSearchEndpoint(endpoint string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return fmt.Errorf("search endpoint not configured — nothing to probe")
	}
	parsed, err := url.Parse(ep)
	if err != nil {
		return fmt.Errorf("parse search endpoint: %w", err)
	}
	if parsed.Host == "" {
		return fmt.Errorf("search endpoint must include a host (configured as %s)", SanitizeEndpoint(ep))
	}
	parsed.User = nil
	parsed.Path = JoinSearchPath(parsed.Path)
	parsed.RawQuery = "q=" + probeQuery + "&format=json"

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return fmt.Errorf("build probe request: %w", err)
	}
	req.Header.Set("User-Agent", "famclaw-startup-check/1")
	// A bare client — no allowlist enforcement here. This is a startup
	// diagnostic, not the tool; the tool enforces the allowlist at call
	// time.
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return newClassified(ErrTimeout, fmt.Sprintf("probe timed out after %s (endpoint is slow, not necessarily down — self-hosted engines fan out to external sites): %s", timeout, err.Error()), 0)
		}
		return newClassified(ErrConnection, fmt.Sprintf("probe could not connect (host is down or refusing connections): %s", err.Error()), 0)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, probeBodyCap+1))
	if err != nil {
		return newClassified(ErrConnection, fmt.Sprintf("read probe response: %s", err.Error()), 0)
	}
	if len(body) > probeBodyCap {
		body = body[:probeBodyCap]
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// The 2xx path is where the real JSON check happens: an HTML
		// welcome page (format=json not enabled) or a broken JSON body
		// would have passed the old any-status probe.
		var payload struct {
			Results json.RawMessage `json:"results"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			if mt, _, perr := mime.ParseMediaType(resp.Header.Get("Content-Type")); perr == nil && mt == "application/json" {
				return newClassified(ErrMalformed, "probe got HTTP 2xx marked application/json, but the body is not a JSON object with a \"results\" field — the search endpoint response is broken", 0)
			}
			return newClassified(ErrMalformed, fmt.Sprintf("probe got HTTP 2xx with content type %q, not application/json — is the endpoint actually a JSON search API (is SearXNG's format=json enabled?)", resp.Header.Get("Content-Type")), 0)
		}
		if len(payload.Results) == 0 {
			return newClassified(ErrMalformed, "probe got HTTP 2xx JSON, but the response has no \"results\" field — check that the path points at the search endpoint", 0)
		}
		return nil
	}

	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return newClassified(ErrAuthConfig, fmt.Sprintf("probe got HTTP %d — the search endpoint rejected the request (auth or configuration problem)", resp.StatusCode), 0)
	case resp.StatusCode == 429:
		return newClassified(ErrRateLimited, "probe got HTTP 429 — the search endpoint is rate-limiting the startup probe (an upstream engine bot-block is common)", 0)
	case resp.StatusCode >= 500:
		return newClassified(ErrProvider5xx, fmt.Sprintf("probe got HTTP %d — the search backend is returning server errors", resp.StatusCode), 0)
	default:
		return newClassified(ErrUnavailable, fmt.Sprintf("probe got HTTP %d — check the endpoint path and configuration", resp.StatusCode), 0)
	}
}
