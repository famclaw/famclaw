package agentcore

import "testing"

// TestIsSafeWebSearchMarker_AllConstants: every marker constant is accepted
// by the allowlist. A new WebSearchMarker* that is missing from the
// IsSafeWebSearchMarker switch fails here.
func TestIsSafeWebSearchMarker_AllConstants(t *testing.T) {
	markers := []string{
		WebSearchMarkerHostNotPermitted,
		WebSearchMarkerProviderNotConfigured,
		WebSearchMarkerRateLimited,
		WebSearchMarkerBackendError,
		WebSearchMarkerTimeout,
		WebSearchMarkerBackendUnreachable,
		WebSearchMarkerBackendAuthConfig,
		WebSearchMarkerInvalidResponse,
		WebSearchMarkerUnavailable,
	}
	for _, m := range markers {
		if !IsSafeWebSearchMarker(m) {
			t.Errorf("IsSafeWebSearchMarker(%q) = false, want true — constant missing from the switch", m)
		}
	}
}

// TestToolErrorDisplay: the truthful failure display shared by the parent's
// tool loop and the subagent dispatch. ReadOnly overrides the generic
// warning; a safe web_search marker renders verbatim (and wins over
// ReadOnly, mirroring the tool loop's check order); everything else keeps
// the generic warning.
func TestToolErrorDisplay(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		readOnly bool
		result   string
		want     string
	}{
		{"generic", "builtin__todo", false, "", ToolErrorDisplayChangesMayHaveOccurred},
		{"generic_ignores_unsafe_result", "builtin__web_fetch", false, "searxng: HTTP 500: dial 10.0.0.7:8080", ToolErrorDisplayChangesMayHaveOccurred},
		{"read_only", "builtin__todo", true, "", ToolErrorDisplayReadOnly},
		{"web_search_marker_rate_limited", "builtin__web_search", false, WebSearchMarkerRateLimited, WebSearchMarkerRateLimited},
		{"web_search_marker_unavailable", "builtin__web_search", false, WebSearchMarkerUnavailable, WebSearchMarkerUnavailable},
		{"web_search_marker_wins_over_read_only", "builtin__web_search", true, WebSearchMarkerTimeout, WebSearchMarkerTimeout},
		{"web_search_unsafe_result_falls_back", "builtin__web_search", false, "web_search: rate limited: 429 from 10.0.0.7", ToolErrorDisplayChangesMayHaveOccurred},
		{"web_search_empty_result_falls_back", "builtin__web_search", false, "", ToolErrorDisplayChangesMayHaveOccurred},
		{"non_search_marker_not_rendered", "builtin__web_fetch", false, WebSearchMarkerRateLimited, ToolErrorDisplayChangesMayHaveOccurred},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolErrorDisplay(tc.toolName, tc.readOnly, tc.result); got != tc.want {
				t.Errorf("ToolErrorDisplay(%q, %v, %q) = %q, want %q", tc.toolName, tc.readOnly, tc.result, got, tc.want)
			}
		})
	}
}

// TestIsSafeWebSearchMarker_RejectsUnknown: anything that is not exactly one
// of the fixed markers must fall through to the generic error display —
// including diagnostic text, near-miss rewordings, and case variants.
func TestIsSafeWebSearchMarker_RejectsUnknown(t *testing.T) {
	unknown := []string{
		"",
		"web_search: empty query",                // provider-internal failure, not a marker
		"web_search: unavailable (extra detail)", // marker plus diagnostic text
		"web_search: Unavailable",                // case variant
		"web_search: rate limited: 429 from 10.0.0.7",   // marker plus host
		"Tool reported an error. No changes were made.", // former generic display
		"web_search: provider server error (5xx)",       // sentinel Error() text, not a marker
	}
	for _, s := range unknown {
		if IsSafeWebSearchMarker(s) {
			t.Errorf("IsSafeWebSearchMarker(%q) = true, want false", s)
		}
	}
}
