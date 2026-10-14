package config

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

// TestResolveWebSearchAPIKey mirrors TestResolveLLMAPIKey for the web_search
// provider key (FAMCLAW_WEB_SEARCH_API_KEY over tools.web_search.tavily_api_key):
// the env var wins, a YAML-only value is used with a warning, and the key
// value itself is never logged.
func TestResolveWebSearchAPIKey(t *testing.T) {
	tests := []struct {
		name        string
		yamlKey     string
		envKey      string
		wantKey     string
		wantWarning bool
	}{
		{name: "env set yaml unset → env wins", yamlKey: "", envKey: "tvly-env-only", wantKey: "tvly-env-only", wantWarning: false},
		{name: "env set yaml set → env wins, no warning", yamlKey: "«redacted:tvly-…»", envKey: "tvly-env-wins", wantKey: "tvly-env-wins", wantWarning: false},
		{name: "env unset yaml set → yaml used, warning logged", yamlKey: "«redacted:tvly-…»", envKey: "", wantKey: "«redacted:tvly-…»", wantWarning: true},
		{name: "env unset yaml unset → empty, no warning", yamlKey: "", envKey: "", wantKey: "", wantWarning: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log.SetOutput(&buf)
			defer log.SetOutput(os.Stderr)

			origEnv, envSet := os.LookupEnv("FAMCLAW_WEB_SEARCH_API_KEY")
			if envSet {
				defer os.Setenv("FAMCLAW_WEB_SEARCH_API_KEY", origEnv)
			} else {
				defer os.Unsetenv("FAMCLAW_WEB_SEARCH_API_KEY")
			}
			if tt.envKey != "" {
				os.Setenv("FAMCLAW_WEB_SEARCH_API_KEY", tt.envKey)
			} else {
				os.Unsetenv("FAMCLAW_WEB_SEARCH_API_KEY")
			}

			c := &Config{Tools: ToolsConfig{WebSearch: WebSearchConfig{TavilyAPIKey: tt.yamlKey}}}
			resolveWebSearchAPIKey(c)

			if c.Tools.WebSearch.TavilyAPIKey != tt.wantKey {
				t.Errorf("TavilyAPIKey = %q, want %q", c.Tools.WebSearch.TavilyAPIKey, tt.wantKey)
			}
			logOutput := buf.String()
			if hasWarning := strings.Contains(logOutput, "plaintext YAML"); hasWarning != tt.wantWarning {
				t.Errorf("warning present = %v, want %v (log: %q)", hasWarning, tt.wantWarning, logOutput)
			}
			// The key value must never be logged.
			if tt.yamlKey != "" && strings.Contains(logOutput, tt.yamlKey) {
				t.Errorf("log output contains the YAML key value: %q", logOutput)
			}
			if tt.envKey != "" && strings.Contains(logOutput, tt.envKey) {
				t.Errorf("log output contains the env key value: %q", logOutput)
			}
		})
	}
}

// TestValidate_WebSearchProvider covers the provider dispatch in
// Config.Validate (web-retrieval plan, Slice 1): the SearXNG default keeps its
// existing endpoint + web_fetch requirements byte-for-byte; the hosted Tavily
// path needs only a key (no endpoint, no url_allowlist entry — a different
// surface); an unknown provider is rejected.
func TestValidate_WebSearchProvider(t *testing.T) {
	// base returns a minimal config that passes every validation except the
	// web_search branch under test (LLM timeout set; nothing else enabled).
	base := func() *Config {
		return &Config{
			// Loopback bind keeps this test focused on the web_search rules;
			// the bind/secret matrix is covered by TestValidate_ServerHost.
			Server: ServerConfig{Host: "127.0.0.1", Port: 8080},
			LLM:    LLMConfig{TimeoutSeconds: 30},
			Tools:  ToolsConfig{},
		}
	}

	t.Run("searxng default without endpoint fails", func(t *testing.T) {
		c := base()
		c.Tools.WebSearch = WebSearchConfig{Enabled: true, Provider: "", MaxResults: 8, TimeoutSec: 30}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "endpoint must be set") {
			t.Errorf("expected the endpoint-required error, got %v", err)
		}
	})

	t.Run("searxng without web_fetch fails", func(t *testing.T) {
		c := base()
		c.Tools.WebSearch = WebSearchConfig{Enabled: true, Endpoint: "http://localhost:8888", MaxResults: 8, TimeoutSec: 30}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "web_fetch") {
			t.Errorf("expected the web_fetch requirement error, got %v", err)
		}
	})

	t.Run("searxng with endpoint and valid web_fetch passes", func(t *testing.T) {
		c := base()
		c.Tools.WebSearch = WebSearchConfig{Enabled: true, Endpoint: "http://localhost:8888", MaxResults: 8, TimeoutSec: 30}
		c.Tools.WebFetch = WebFetchConfig{Enabled: true, MaxBytes: 256 * 1024, TimeoutSec: 30, URLAllowlist: []string{"localhost"}}
		if err := c.Validate(); err != nil {
			t.Errorf("a valid searxng config should pass, got %v", err)
		}
	})

	t.Run("tavily without a key fails", func(t *testing.T) {
		c := base()
		c.Tools.WebSearch = WebSearchConfig{Enabled: true, Provider: "tavily", MaxResults: 8, TimeoutSec: 30}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "tavily_api_key must be set") {
			t.Errorf("expected the tavily-key-required error, got %v", err)
		}
	})

	t.Run("tavily with a key needs no endpoint or web_fetch", func(t *testing.T) {
		c := base()
		c.Tools.WebSearch = WebSearchConfig{Enabled: true, Provider: "tavily", TavilyAPIKey: "tvly-x", MaxResults: 8, TimeoutSec: 30}
		if err := c.Validate(); err != nil {
			t.Errorf("a valid tavily config (no endpoint/web_fetch) should pass, got %v", err)
		}
	})

	t.Run("unknown provider fails", func(t *testing.T) {
		c := base()
		c.Tools.WebSearch = WebSearchConfig{Enabled: true, Provider: "bogus", MaxResults: 8, TimeoutSec: 30}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Errorf("expected the unknown-provider error, got %v", err)
		}
	})
}
