package config

import (
	"strings"
	"testing"
)

// TestValidate_FallbackToBrowser requires web_fetch to be enabled (so the
// browser host-gate exists) and a browser endpoint when fallback_to_browser
// is on. This is a fail-fast guard — without a browser pool the fallback
// silently degrades to thin HTTP text on every JS-heavy fetch.
func TestValidate_FallbackToBrowser(t *testing.T) {
	base := func() *Config {
		return &Config{
			Tools: ToolsConfig{
				WebFetch: WebFetchConfig{
					Enabled:      true,
					URLAllowlist: []string{"example.com"},
					MaxBytes:     256 * 1024,
					TimeoutSec:   15,
				},
			},
			Server: ServerConfig{Host: "0.0.0.0", Port: 8080},
			LLM: LLMConfig{
				MaxContextTokens:  4096,
				MaxResponseTokens: 512,
				TimeoutSeconds:    300,
			},
		}
	}
	cases := []struct {
		name       string
		mut        func(c *Config)
		wantErrSub string // empty = expect no error
	}{
		{
			name:       "fallback on, browser disabled -> error",
			mut:        func(c *Config) { c.Tools.WebFetch.FallbackToBrowser = true },
			wantErrSub: "fallback_to_browser requires tools.browser.enabled",
		},
		{
			name: "fallback off, browser disabled -> ok (default no-browser infra)",
			mut:  func(c *Config) {},
			// wantErrSub empty -> expect no error
		},
		{
			name: "fallback on, browser enabled with endpoint -> ok",
			mut: func(c *Config) {
				c.Tools.WebFetch.FallbackToBrowser = true
				c.Tools.Browser.Enabled = true
				c.Tools.Browser.Endpoint = "ws://localhost:3000/"
				c.Tools.Browser.IdleSec = 300
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mut(c)
			err := c.Validate()
			if tc.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrSub)
				}
				if !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrSub)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
