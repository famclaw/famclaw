package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/webfetch"
	"github.com/famclaw/famclaw/internal/websearch"
)

func TestClassifiedWebSearchErrorFormatsSafelyAndPreservesCause(t *testing.T) {
	const secret = "fake-private-host.internal/path?token=fake-secret"
	host := webfetch.NewHostNotAllowedError(secret)
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{"host", host}, {"configuration", websearch.ErrProviderNotConfigured},
		{"rate limit", websearch.ErrRateLimited}, {"server", websearch.ErrProvider5xx},
		{"timeout", websearch.ErrTimeout}, {"connection", websearch.ErrConnection},
		{"auth", websearch.ErrAuthConfig}, {"malformed", websearch.ErrMalformed}, {"unavailable", websearch.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := fmt.Errorf("%s: %w", secret, tc.cause)
			marker, err := webSearchError(cause)
			if marker == "" || err == nil {
				t.Fatal("classified failure missing marker or error")
			}
			for _, formatted := range []string{fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err)} {
				if strings.Contains(formatted, secret) || formatted != marker {
					t.Error("formatted error is not the fixed safe marker")
				}
			}
			if !errors.Is(err, cause) || !errors.Is(err, tc.cause) {
				t.Error("original cause or classification no longer unwraps")
			}
			if tc.name == "host" {
				var got *webfetch.HostNotAllowedError
				if !errors.As(err, &got) || got != host {
					t.Error("original typed cause not preserved")
				}
			}
		})
	}
	ordinary := errors.New(secret)
	if marker, err := webSearchError(ordinary); marker != "" || err != ordinary {
		t.Error("unclassified error identity changed")
	}
	if marker, err := webSearchError(nil); marker != "" || err != nil {
		t.Error("nil behavior changed")
	}
}
