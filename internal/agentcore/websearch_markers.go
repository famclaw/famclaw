package agentcore

// Fixed, non-diagnostic error markers for the web_search tool.
//
// These constants are the single source of truth for the marker vocabulary
// shared by the producer (agent.webSearchError, internal/agent/agent.go) and
// the consumer (the tool loop's allowlist check, IsSafeWebSearchMarker, used
// from stage_tool_loop.go). The producer returns exactly one of these
// strings — or "" for unclassified errors — when a builtin__web_search call
// fails; the tool loop renders the tool output to the LLM verbatim only when
// IsSafeWebSearchMarker accepts it.
//
// No marker carries diagnostic detail (endpoint, host, cause text): anything
// that could leak a private network address or error detail stays in the
// returned error, never in the marker.
//
// Adding a new classified failure to agent.webSearchError: define its marker
// here, return the constant, and add it to the IsSafeWebSearchMarker switch
// below — both edits in this file. The agent-side test
// TestWebSearchError_ClassMarkers additionally fails if a producer marker is
// ever missing from the allowlist.
//
// See PR #431 (PR-Agent review): the two former copies of this vocabulary
// (literals in agent.go and a hardcoded allowlist in stage_tool_loop.go)
// could silently drift apart.
const (
	// WebSearchMarkerHostNotPermitted: host rejected by the url_allowlist gate.
	WebSearchMarkerHostNotPermitted = "web_search: host not permitted"
	// WebSearchMarkerProviderNotConfigured: provider-boundary configuration
	// gap (unknown provider name or hosted provider without a key).
	WebSearchMarkerProviderNotConfigured = "web_search: provider not configured"
	// WebSearchMarkerRateLimited: backend answered 429.
	WebSearchMarkerRateLimited = "web_search: rate limited"
	// WebSearchMarkerBackendError: provider server error (5xx).
	WebSearchMarkerBackendError = "web_search: search backend error"
	// WebSearchMarkerTimeout: request exceeded its deadline.
	WebSearchMarkerTimeout = "web_search: search timed out"
	// WebSearchMarkerBackendUnreachable: connection failure (refused/DNS/reset).
	WebSearchMarkerBackendUnreachable = "web_search: search backend unreachable"
	// WebSearchMarkerBackendAuthConfig: provider auth/config error (401/403).
	WebSearchMarkerBackendAuthConfig = "web_search: search backend auth or config error"
	// WebSearchMarkerInvalidResponse: malformed/non-JSON backend response.
	WebSearchMarkerInvalidResponse = "web_search: invalid search backend response"
	// WebSearchMarkerUnavailable: undetermined backend failure (umbrella).
	WebSearchMarkerUnavailable = "web_search: unavailable"
)

// IsSafeWebSearchMarker accepts only the fixed, non-diagnostic search
// classifications listed above. Anything else — including unclassified
// errors, near-miss rewordings, and markers that carry extra detail —
// falls through to the generic error display.
func IsSafeWebSearchMarker(s string) bool {
	switch s {
	case WebSearchMarkerHostNotPermitted, WebSearchMarkerProviderNotConfigured,
		WebSearchMarkerRateLimited, WebSearchMarkerBackendError,
		WebSearchMarkerTimeout, WebSearchMarkerBackendUnreachable,
		WebSearchMarkerBackendAuthConfig, WebSearchMarkerInvalidResponse,
		WebSearchMarkerUnavailable:
		return true
	default:
		return false
	}
}
