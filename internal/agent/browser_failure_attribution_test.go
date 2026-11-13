package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/browser"
	"github.com/famclaw/famclaw/internal/config"
)

type failingBrowserForAttribution struct{ err error }

func (f failingBrowserForAttribution) Exec(context.Context, browser.ExecInput) (string, error) {
	return "", f.err
}

func (f failingBrowserForAttribution) NavigateAndExtract(context.Context, string, string, browser.HostValidator) (string, int64, error) {
	return "", 0, f.err
}

func TestBrowserFailureAttributionAndPrivacy(t *testing.T) {
	for _, tt := range []struct {
		name     string
		tool     string
		cause    error
		category string
	}{
		{"generic", "builtin__browser_navigate", errors.New("fake-private-error"), "failed"},
		{"cancelled", "builtin__browser_click", fmt.Errorf("fake-private-error: %w", context.Canceled), "cancelled"},
		{"timeout", "builtin__browser_fill", fmt.Errorf("fake-private-error: %w", context.DeadlineExceeded), "timeout"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(old) })
			a := &Agent{user: &config.UserConfig{Name: "example-user"}, browserPool: failingBrowserForAttribution{err: tt.cause}}
			out, err := a.makeBuiltinHandler()(context.Background(), tt.tool, map[string]any{"url": "https://example.invalid/fake-private-url", "value": "fake-private-argument"})
			if out != "" || !errors.Is(err, tt.cause) {
				t.Fatalf("result or original error changed: output=%q error=%v", out, err)
			}
			got := logs.String()
			for _, want := range []string{"example-user", tt.tool, tt.category} {
				if !strings.Contains(got, want) {
					t.Errorf("log missing %q: %q", want, got)
				}
			}
			for _, private := range []string{"fake-private-error", "fake-private-url", "fake-private-argument"} {
				if strings.Contains(got, private) {
					t.Errorf("log leaked private data %q", private)
				}
			}
		})
	}
}
