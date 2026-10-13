package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/webfetch"
	"github.com/famclaw/famclaw/internal/websearch"
)

// TestWebSearchError_ClassMarkers is the Slice 2d contract: each classified
// backend failure maps to its own short neutral marker (nil error, so the
// tool loop hands it to the LLM as ordinary output instead of an "Error:"
// blob), the pre-class special cases keep their existing markers, and
// anything unclassified passes through as a plain error. Every marker is
// neutral English and must never leak the endpoint (hostnames,
// credentials, network topology stay server-side).
func TestWebSearchError_ClassMarkers(t *testing.T) {
	const endpoint = "http://10.0.0.7:8080"

	cases := []struct {
		name    string
		err     error
		want    string
		wantErr bool // true: the error must pass through non-nil
	}{
		{
			name: "rate limited (429)",
			err:  fmt.Errorf("tavily: search: %w", websearch.ErrRateLimited),
			want: "web_search: rate limited",
		},
		{
			name: "provider 5xx",
			err:  fmt.Errorf("searxng: search: %w", websearch.ErrProvider5xx),
			want: "web_search: search backend error",
		},
		{
			name: "request timeout",
			err:  fmt.Errorf("searxng: %w", websearch.ErrTimeout),
			want: "web_search: search timed out",
		},
		{
			name: "connection failure",
			err:  fmt.Errorf("searxng: dial tcp 10.0.0.7:8080: %w", websearch.ErrConnection),
			want: "web_search: search backend unreachable",
		},
		{
			name: "auth or config (401/403)",
			err:  fmt.Errorf("searxng: %w", websearch.ErrAuthConfig),
			want: "web_search: search backend auth or config error",
		},
		{
			name: "malformed backend response",
			err:  fmt.Errorf("searxng: %w", websearch.ErrMalformed),
			want: "web_search: invalid search backend response",
		},
		{
			name: "undetermined umbrella residual",
			err:  fmt.Errorf("searxng: HTTP 404: %w", websearch.ErrUnavailable),
			want: "web_search: unavailable",
		},
		{
			name: "host not allowed (web_fetch allowlist gap)",
			err:  webfetch.NewHostNotAllowedError("evil.com"),
			want: "web_search: host not permitted",
		},
		{
			name: "hosted provider without a key",
			err:  providerForGap(t, "tavily"),
			want: "web_search: provider not configured",
		},
		{
			name: "unknown provider",
			err:  providerForGap(t, "bogus"),
			want: "web_search: provider not configured",
		},
		{
			name:    "unclassified error passes through",
			err:     fmt.Errorf("boom: something the classes do not cover"),
			want:    "",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := webSearchError(tc.err, endpoint)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected the error to pass through, got nil (msg=%q)", msg)
				}
				if msg != "" {
					t.Errorf("passthrough must keep an empty result, got %q", msg)
				}
				return
			}
			if err != nil {
				t.Fatalf("classified failure must return a nil error so the tool loop hands the marker to the LLM, got %v", err)
			}
			if msg != tc.want {
				t.Errorf("marker = %q, want %q", msg, tc.want)
			}
			// The endpoint (and anything it carries) must never reach the
			// family chat via the marker.
			for _, secret := range []string{endpoint, "10.0.0.7"} {
				if strings.Contains(msg, secret) {
					t.Errorf("marker leaks the endpoint: %q", msg)
				}
			}
			// The marker is a terse fact, not a pre-written sentence
			// addressed to the user — the model composes the family-facing
			// wording in the family's language.
			if strings.Contains(msg, "I could not") || strings.Contains(msg, "A parent can") {
				t.Errorf("marker must not be a pre-written user-facing sentence: %q", msg)
			}
		})
	}
}

// providerForGap builds the real configuration-gap error via the same path
// the tool uses at runtime, so this test exercises the actual
// sentinel-wrapped error (not a copy of its message): the marker branch
// classifies by websearch.ErrProviderNotConfigured via errors.Is, and this
// helper proves the real error carries it.
func providerForGap(t *testing.T, name string) error {
	t.Helper()
	_, err := websearch.ProviderFor(websearch.ProviderConfig{Name: name})
	if err == nil {
		t.Fatalf("ProviderFor(%q) unexpectedly succeeded", name)
	}
	return err
}

// TestWebSearchError_ClassBeforeUmbrella: a rate-limited error ALSO wraps
// the ErrUnavailable umbrella (Slice 2a legacy compatibility). The marker
// switch must return the class-specific "rate limited" marker, not fall
// through to the umbrella's generic "unavailable" one.
func TestWebSearchError_ClassBeforeUmbrella(t *testing.T) {
	msg, err := webSearchError(&dualError{class: websearch.ErrRateLimited, umbrella: websearch.ErrUnavailable}, "http://search.local")
	if err != nil {
		t.Fatalf("expected a nil error with a marker, got %v", err)
	}
	if msg != "web_search: rate limited" {
		t.Errorf("class-specific checks must run before the umbrella: got %q, want the rate-limited marker", msg)
	}
}

// dualError mimics the shape of a real classified error: it matches the
// given class AND the ErrUnavailable umbrella, exactly like *searchError.
type dualError struct {
	class    error
	umbrella error
}

func (d *dualError) Error() string { return "web_search: " + d.class.Error() }

func (d *dualError) Unwrap() []error { return []error{d.class, d.umbrella} }

// TestWebSearchError_NoPanicOnNil: a nil error must not panic; it passes
// through as ("", nil).
func TestWebSearchError_NoPanicOnNil(t *testing.T) {
	msg, err := webSearchError(nil, "http://search.local")
	if err != nil {
		t.Fatalf("nil in, got error %v", err)
	}
	if msg != "" {
		t.Errorf("nil in, got marker %q, want empty", msg)
	}
}

// TestWebSearchError_SentinelSurvivesReworkedWording: the
// "provider not configured" mapping keys on the
// websearch.ErrProviderNotConfigured sentinel, never on message text.
// Re-wrapping the configuration gap with new wording (what an upstream
// rework would produce) must still land on the marker; conversely, an error
// carrying the historical wording but no sentinel must NOT be treated as a
// configuration gap — it did not come from the provider boundary, so it
// passes through for the tool loop to surface.
func TestWebSearchError_SentinelSurvivesReworkedWording(t *testing.T) {
	t.Run("re-wrapped with new wording still maps to the marker", func(t *testing.T) {
		inner := providerForGap(t, "tavily")
		// Simulate an upstream rework: entirely new wording, same sentinel.
		reworked := fmt.Errorf("web_search: provider setup failed: %w", inner)
		msg, err := webSearchError(reworked, "http://10.0.0.7:8080")
		if err != nil {
			t.Fatalf("expected a sanitized marker, got error %v", err)
		}
		if msg != "web_search: provider not configured" {
			t.Errorf("marker = %q, want %q — classification must key on the sentinel, not the wording",
				msg, "web_search: provider not configured")
		}
	})

	t.Run("historical wording without the sentinel passes through", func(t *testing.T) {
		// The literal text the old string-match branch keyed on, built as a
		// plain error. Without the sentinel it is not a provider-boundary
		// configuration gap and must pass through (surfaced by the tool
		// loop), not be silently labelled one.
		plain := fmt.Errorf("web_search: tavily provider selected but no API key is set (env FAMCLAW_WEB_SEARCH_API_KEY or tools.web_search.tavily_api_key)")
		msg, err := webSearchError(plain, "")
		if err == nil {
			t.Fatalf("expected the error to pass through, got marker %q", msg)
		}
		if msg != "" {
			t.Errorf("passthrough must keep an empty result, got %q", msg)
		}
	})
}
