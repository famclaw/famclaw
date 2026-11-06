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

// Canonical display strings for tool failures. A failed tool call is
// reported to the model with exactly one of these: the read-only variant
// when the tool is declared ReadOnly in the registry (no state could have
// changed), otherwise the generic variant (side effects are possible).
// builtin__web_search additionally may render its fixed classification
// marker verbatim (see ToolErrorDisplay below).
const (
	// ToolErrorDisplayNoChanges: failure of a ReadOnly tool.
	ToolErrorDisplayNoChanges = "Tool reported an error. No changes were made."
	// ToolErrorDisplayChangesMayHaveOccurred: failure of a tool that may
	// mutate state; the model is told to verify before retrying.
	ToolErrorDisplayChangesMayHaveOccurred = "Tool reported an error; some changes may still have occurred. Verify before trying again."
)

// ToolErrorDisplay computes the truthful failure display for a tool call
// that reported an error:
//   - a tool declared ReadOnly is reported as having made no changes;
//   - a builtin__web_search failure whose result is a safe classification
//     marker (IsSafeWebSearchMarker) is rendered verbatim with that specific
//     classification;
//   - everything else gets the generic "changes may have occurred" warning.
//
// This is the single source of truth shared by the parent agent's tool loop
// (stage_tool_loop.go) and the subagent's tool dispatch
// (internal/subagent/executor.go), so both surfaces classify the same
// failure identically.
func ToolErrorDisplay(toolName string, readOnly bool, result string) string {
	display := ToolErrorDisplayChangesMayHaveOccurred
	if readOnly {
		display = ToolErrorDisplayNoChanges
	}
	if toolName == "builtin__web_search" && IsSafeWebSearchMarker(result) {
		display = result
	}
	return display
}

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
