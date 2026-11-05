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

// TestIsSafeWebSearchMarker_RejectsUnknown: anything that is not exactly one
// of the fixed markers must fall through to the generic error display —
// including diagnostic text, near-miss rewordings, and case variants.
func TestIsSafeWebSearchMarker_RejectsUnknown(t *testing.T) {
	unknown := []string{
		"",
		"web_search: empty query",                      // provider-internal failure, not a marker
		"web_search: unavailable (extra detail)",       // marker plus diagnostic text
		"web_search: Unavailable",                      // case variant
		"web_search: rate limited: 429 from 10.0.0.7",  // marker plus host
		"Tool reported an error. No changes were made.", // generic display itself
		"web_search: provider server error (5xx)",      // sentinel Error() text, not a marker
	}
	for _, s := range unknown {
		if IsSafeWebSearchMarker(s) {
			t.Errorf("IsSafeWebSearchMarker(%q) = true, want false", s)
		}
	}
}
