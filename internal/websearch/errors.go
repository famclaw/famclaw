package websearch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Typed error classes for the web_search tool (web-retrieval plan, Slice 2a).
//
// Before this slice every failure collapsed into ErrUnavailable, so the model
// could not tell "rate-limited" from "malformed" from "server down". Each
// sentinel below is a distinct outcome; callers match via errors.Is.
//
// Classification happens at the provider boundary: each adapter maps its HTTP
// status / transport error to exactly one class. webfetch.HostNotAllowedError
// is NOT a search-provider error — it stays a configuration error and is
// returned as-is by Search (the parent fixes it in url_allowlist).
//
// ErrUnavailable is kept as the umbrella that every class below wraps:
// existing errors.Is(err, ErrUnavailable) callers (the agent's
// webSearchError) keep working, and new code matches the specific sentinel.
// It also remains the "could not determine which" fallback for statuses that
// map to no specific class.
//
// ErrProviderNotConfigured is the ONE exception that does not wrap
// ErrUnavailable: it is not a backend failure, it is an operator
// configuration gap detected at the provider boundary (ProviderFor) — an
// unsupported provider name, or a hosted provider selected without its API
// key. Unlike ErrAuthConfig (a 401/403 answered by a backend that was
// actually reached), the request never goes out: the tool cannot be
// configured yet, and the operator fixes the config (provider name / API
// key). It is never transient — retrying cannot fix a config gap — so it
// stays out of isTransient. Callers (the agent's webSearchError) match it
// via errors.Is, never by message text.
var (
	ErrUnavailable           = errors.New("web_search: search backend unavailable")
	ErrProviderNotConfigured = errors.New("web_search: provider not configured")
	ErrAuthConfig            = errors.New("web_search: provider auth/config error (bad/missing key, 401/403)")
	ErrRateLimited           = errors.New("web_search: provider rate limit (429)")
	ErrProvider5xx           = errors.New("web_search: provider server error (5xx)")
	ErrMalformed             = errors.New("web_search: provider returned malformed/non-JSON response")
	ErrConnection            = errors.New("web_search: connection failure (refused/DNS/reset)")
	ErrTimeout               = errors.New("web_search: search timed out")
)

// searchError is the concrete search failure. It wraps BOTH the
// ErrUnavailable umbrella and one specific class sentinel, so a single error
// answers both errors.Is(err, websearch.ErrUnavailable) (legacy callers) and
// errors.Is(err, websearch.ErrRateLimited) (new code).
//
// Error() renders the specific class plus a short neutral detail — never the
// API key, never the provider response body, never raw endpoint credentials
// (endpoints in logs go through SanitizeEndpoint instead).
type searchError struct {
	class      error // one of the class sentinels above
	detail     string
	retryAfter time.Duration // provider's Retry-After, when one was sent
}

func (e *searchError) Error() string {
	if strings.TrimSpace(e.detail) == "" {
		return e.class.Error()
	}
	return e.class.Error() + ": " + e.detail
}

// Unwrap exposes both the umbrella and the specific class so errors.Is
// matches either (multi-error unwrapping, Go 1.20+).
func (e *searchError) Unwrap() []error {
	return []error{ErrUnavailable, e.class}
}

// RetryAfter returns the provider's Retry-After delay (0 when none was
// sent). The retry decorator (Slice 2b) honors it when present.
func (e *searchError) RetryAfter() time.Duration { return e.retryAfter }

// newClassified builds a typed search failure. detail must be short and
// neutral (see searchError); an empty detail renders just the class text.
func newClassified(class error, detail string, retryAfter time.Duration) error {
	if retryAfter < 0 {
		retryAfter = 0
	}
	return &searchError{class: class, detail: detail, retryAfter: retryAfter}
}

// classifyHTTP maps an HTTP status to exactly one class sentinel and
// carries the provider's Retry-After (when sent). 401/403 → auth/config,
// 429 → rate limit, 5xx → provider server error. Other statuses (400, 404,
// 405, ...) are configuration/misuse of the endpoint — not transient, not
// auth — and fall back to the ErrUnavailable umbrella with a detail, so they
// are reported honestly without being retried.
func classifyHTTP(status int, retryAfter time.Duration) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return newClassified(ErrAuthConfig, fmt.Sprintf("HTTP %d", status), 0)
	case status == http.StatusTooManyRequests:
		return newClassified(ErrRateLimited, fmt.Sprintf("HTTP %d", status), retryAfter)
	case status >= 500 && status < 600:
		return newClassified(ErrProvider5xx, fmt.Sprintf("HTTP %d", status), 0)
	}
	return newClassified(ErrUnavailable, fmt.Sprintf("HTTP %d", status), 0)
}

// transportClass maps a failed request (no HTTP response received) to its
// class sentinel: a deadline/timeout → ErrTimeout, anything else (canceled,
// refused, DNS, reset) → ErrConnection.
func transportClass(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		// Client.Timeout (http.Client.Timeout) produces a net.Error with
		// Timeout()==true that is not necessarily is-able with
		// context.DeadlineExceeded — check both.
		return ErrTimeout
	}
	return ErrConnection
}

// classifyTransport is transportClass plus a class-appropriate detail.
func classifyTransport(err error) error {
	if transportClass(err) == ErrTimeout {
		return newClassified(ErrTimeout, "request timed out", 0)
	}
	if errors.Is(err, context.Canceled) {
		return newClassified(ErrConnection, "request canceled before completion", 0)
	}
	return newClassified(ErrConnection, err.Error(), 0)
}

// retryAfterFromHeader parses a Retry-After header (delta-seconds or
// HTTP-date, per RFC 9110) into a duration; 0 when absent, zero, or
// unparseable. The standard library has no Retry-After parser, so the
// delta-seconds form is parsed by hand.
func retryAfterFromHeader(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// isTransient reports whether err belongs to a class the retry decorator may
// retry (web-retrieval plan, Slice 2b): rate limit, provider 5xx, connection
// failure, or timeout. It NEVER retries auth/config errors, malformed
// responses, host-not-allowed (a configuration gap), or the undetermined
// umbrella — those fail identically on retry.
func isTransient(err error) bool {
	switch {
	case errors.Is(err, ErrRateLimited),
		errors.Is(err, ErrProvider5xx),
		errors.Is(err, ErrConnection),
		errors.Is(err, ErrTimeout):
		return true
	}
	return false
}
