package websearch

import (
	"strings"
	"testing"
)

// TestProviderFor_DefaultSearXNG verifies the search-provider boundary
// (web-retrieval plan, Slice 1): an empty or "searxng" provider selects the
// SearXNG implementation (the backward-compatible default, so an existing
// deployment that sets only tools.web_search.endpoint is unchanged); "tavily"
// requires a key and selects the hosted adapter; any other value is a
// configuration error. ProviderFor only constructs — it does not dial — so
// this test touches no network.
func TestProviderFor_DefaultSearXNG(t *testing.T) {
	t.Run("empty name selects the SearXNG default", func(t *testing.T) {
		p, err := ProviderFor(ProviderConfig{Name: ""})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Name() != "searxng" {
			t.Errorf("empty provider: Name() = %q, want searxng", p.Name())
		}
	})

	t.Run("explicit searxng selects SearXNG", func(t *testing.T) {
		p, err := ProviderFor(ProviderConfig{Name: "searxng", Endpoint: "http://localhost:8888"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Name() != "searxng" {
			t.Errorf("Name() = %q, want searxng", p.Name())
		}
	})

	t.Run("tavily with a key selects Tavily", func(t *testing.T) {
		p, err := ProviderFor(ProviderConfig{Name: "tavily", TavilyAPIKey: "tvly-test-key"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Name() != "tavily" {
			t.Errorf("Name() = %q, want tavily", p.Name())
		}
	})

	t.Run("tavily provider name is case/whitespace-insensitive", func(t *testing.T) {
		p, err := ProviderFor(ProviderConfig{Name: "  Tavily ", TavilyAPIKey: "tvly-test-key"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Name() != "tavily" {
			t.Errorf("Name() = %q, want tavily", p.Name())
		}
	})

	t.Run("tavily without a key is a configuration error", func(t *testing.T) {
		_, err := ProviderFor(ProviderConfig{Name: "tavily"})
		if err == nil {
			t.Fatal("expected an error when tavily is selected without a key")
		}
		if !strings.Contains(err.Error(), "no API key") {
			t.Errorf("error should mention the missing API key, got %v", err)
		}
	})

	t.Run("unknown provider is a configuration error", func(t *testing.T) {
		_, err := ProviderFor(ProviderConfig{Name: "bogus"})
		if err == nil {
			t.Fatal("expected an error for an unknown provider")
		}
		if !strings.Contains(err.Error(), "unknown provider") {
			t.Errorf("error should mention the unknown provider, got %v", err)
		}
	})
}
