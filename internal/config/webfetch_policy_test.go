package config

import (
	"strings"
	"testing"
)

// TestResolvedURLPolicy locks the mode-resolution rule from design §1.3:
// an explicit url_policy wins; otherwise a non-empty allowlist keeps the
// legacy allowlist gate (back-compat) and an empty/absent allowlist resolves
// to the kid-safe denylist default.
func TestResolvedURLPolicy(t *testing.T) {
	tests := []struct {
		name    string
		policy  string
		allow   []string
		deny    []string
		want    string
	}{
		{"explicit allowlist wins", "allowlist", []string{}, []string{}, "allowlist"},
		{"explicit denylist wins", "denylist", []string{"a.com"}, []string{}, "denylist"},
		{"explicit is case/space insensitive", "  DENYLIST  ", []string{}, []string{}, "denylist"},
		{"unset + non-empty allowlist -> allowlist (back-compat)", "", []string{"a.com"}, nil, "allowlist"},
		{"unset + empty allowlist -> denylist (zero-config fix)", "", nil, nil, "denylist"},
		{"unset + whitespace-only allowlist -> denylist", "", []string{"  "}, nil, "denylist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := WebFetchConfig{URLPolicy: tt.policy, URLAllowlist: tt.allow, URLDenylist: tt.deny}
			if got := c.ResolvedURLPolicy(); got != tt.want {
				t.Errorf("ResolvedURLPolicy() = %q, want %q", got, tt.want)
			}
		})
	}
}

// wfValidBase returns the minimal surrounding config that passes every rule
// other than the web_fetch branch under test (mirrors websearch_provider_test.go).
func wfValidBase() *Config {
	return &Config{
		Server: ServerConfig{Host: "127.0.0.1", Port: 8080},
		LLM:    LLMConfig{TimeoutSeconds: 30},
		Tools:  ToolsConfig{},
	}
}

// TestValidate_WebFetchURLPolicy covers the mode-aware allowlist requirement:
// the non-empty-allowlist rule now applies to the EFFECTIVE mode, so a
// zero-config web_fetch (empty allowlist, no policy) validates as the denylist
// default instead of erroring out — the root cause of the "everything is
// blocked from start" bug.
func TestValidate_WebFetchURLPolicy(t *testing.T) {
	t.Run("zero-config (empty allowlist, no policy) passes as denylist default", func(t *testing.T) {
		c := wfValidBase()
		c.Tools.WebFetch = WebFetchConfig{Enabled: true, MaxBytes: 256 * 1024, TimeoutSec: 30}
		if err := c.Validate(); err != nil {
			t.Fatalf("zero-config web_fetch should resolve to denylist and pass, got %v", err)
		}
	})

	t.Run("explicit denylist with empty allowlist passes", func(t *testing.T) {
		c := wfValidBase()
		c.Tools.WebFetch = WebFetchConfig{Enabled: true, URLPolicy: "denylist", URLDenylist: []string{"bad.example.com"}, MaxBytes: 256 * 1024, TimeoutSec: 30}
		if err := c.Validate(); err != nil {
			t.Fatalf("explicit denylist with empty allowlist should pass, got %v", err)
		}
	})

	t.Run("non-empty allowlist (policy unset) passes as allowlist back-compat", func(t *testing.T) {
		c := wfValidBase()
		c.Tools.WebFetch = WebFetchConfig{Enabled: true, URLAllowlist: []string{"localhost"}, MaxBytes: 256 * 1024, TimeoutSec: 30}
		if err := c.Validate(); err != nil {
			t.Fatalf("non-empty allowlist should pass (allowlist mode), got %v", err)
		}
	})

	t.Run("explicit allowlist + empty allowlist fails (SSRF guard kept)", func(t *testing.T) {
		c := wfValidBase()
		c.Tools.WebFetch = WebFetchConfig{Enabled: true, URLPolicy: "allowlist", MaxBytes: 256 * 1024, TimeoutSec: 30}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "url_allowlist must list at least one") {
			t.Fatalf("explicit allowlist + empty list must fail, got %v", err)
		}
	})

	t.Run("invalid url_policy value fails", func(t *testing.T) {
		c := wfValidBase()
		c.Tools.WebFetch = WebFetchConfig{Enabled: true, URLPolicy: "bogus", URLAllowlist: []string{"x.com"}, MaxBytes: 256 * 1024, TimeoutSec: 30}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "url_policy must be") {
			t.Fatalf("invalid url_policy must fail, got %v", err)
		}
	})

	t.Run("disabled web_fetch imposes no allowlist requirement", func(t *testing.T) {
		c := wfValidBase()
		c.Tools.WebFetch = WebFetchConfig{Enabled: false}
		if err := c.Validate(); err != nil {
			t.Fatalf("disabled web_fetch should not require an allowlist, got %v", err)
		}
	})
}
