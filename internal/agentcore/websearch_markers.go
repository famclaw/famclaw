package agentcore

import (
	"context"
	"errors"
	"io/fs"
	"strings"
)

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
// when the tool is marked ReadOnly in the registry, otherwise the generic
// variant (side effects are possible). A read-only declaration is not proof
// that a call had no effects.
// builtin__web_search additionally may render its fixed classification
// marker verbatim (see ToolErrorDisplay below).
const (
	// ToolErrorDisplayReadOnly describes the declaration without asserting effects.
	ToolErrorDisplayReadOnly = "Tool reported an error. It is marked read-only; its effects are not verified."
	// ToolErrorDisplayChangesMayHaveOccurred: failure of a tool that may
	// mutate state; the model is told to verify before retrying.
	ToolErrorDisplayChangesMayHaveOccurred = "Tool reported an error; some changes may still have occurred. Verify before trying again."
)

// ToolErrorDisplay computes the truthful failure display for a tool call
// that reported an error:
//   - a tool marked ReadOnly is described without asserting its effects;
//   - a builtin__web_search failure whose result is a safe classification
//     marker (IsSafeWebSearchMarker) is rendered verbatim with that specific
//     classification;
//   - everything else gets the generic "changes may have occurred" warning.
//
// This is the single source of truth shared by the parent agent's tool loop
// (stage_tool_loop.go) and the subagent's tool dispatch
// (internal/subagent/executor.go), so both surfaces classify the same
// failure identically. Both stage_tool_loop.go and subagent/executor.go
// normalize allowlisted bare builtin names before calling this helper.
func ToolErrorDisplay(toolName string, readOnly bool, result string) string {
	display := ToolErrorDisplayChangesMayHaveOccurred
	if readOnly {
		display = ToolErrorDisplayReadOnly
	}
	if toolName == "builtin__web_search" && IsSafeWebSearchMarker(result) {
		display = result
	}
	return display
}

// ToolErrorDisplayWithCause adds a fixed error class for builtins when the
// cause matches a standard sentinel. It never formats the error or raw result.
// The existing uncertainty warning and exact web-search marker policy remain.
func ToolErrorDisplayWithCause(toolName string, readOnly bool, result string, err error) string {
	display := ToolErrorDisplay(toolName, readOnly, result)
	if !strings.HasPrefix(toolName, "builtin__") || toolName == "builtin__web_search" {
		return display
	}
	var class string
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		class = "timeout"
	case errors.Is(err, context.Canceled):
		class = "cancelled"
	case errors.Is(err, fs.ErrNotExist):
		class = "not found"
	case errors.Is(err, fs.ErrPermission):
		class = "permission denied"
	default:
		return display
	}
	return display + " Error class: " + class + "."
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
