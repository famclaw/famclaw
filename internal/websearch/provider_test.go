package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProviderFor_SearXNGEndpointReachesSearch is a regression test for the
// "web_search: endpoint not configured" bug: ProviderFor stored the endpoint
// only on the searxngProvider struct and omitted it from the Options it
// hands to Search, so every call failed the opts.Endpoint == "" check even
// with tools.web_search.endpoint set. A provider built by ProviderFor must
// actually reach the endpoint: this test points one at an httptest server
// and requires a request to arrive and hits to come back. It fails without
// the Endpoint wiring in ProviderFor's searxng branch.
func TestProviderFor_SearXNGEndpointReachesSearch(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]string{
				{"title": "T1", "url": "https://e1", "content": "S1"},
			},
		})
	}))
	defer server.Close()

	p, err := ProviderFor(ProviderConfig{
		Name:                 "searxng",
		Endpoint:             server.URL,
		MaxResults:           5,
		AllowPrivateNetworks: true, // httptest listens on 127.0.0.1
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hits, err := p.Search(context.Background(), "regression query", 5)
	if err != nil {
		t.Fatalf("provider built by ProviderFor must not fail configuration checks, got: %v", err)
	}
	if gotQuery != "regression query" {
		t.Errorf("endpoint never received the query (got q=%q) — request did not reach the backend", gotQuery)
	}
	if len(hits) != 1 || hits[0].Title != "T1" {
		t.Errorf("hits = %+v, want one hit titled T1", hits)
	}
}

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
		if !errors.Is(err, ErrProviderNotConfigured) {
			t.Errorf("missing-key error must wrap ErrProviderNotConfigured (errors.Is classification), got %v", err)
		}
		if errors.Is(err, ErrUnavailable) {
			t.Errorf("a config gap is not a backend failure; it must not wrap the ErrUnavailable umbrella: %v", err)
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
		if !errors.Is(err, ErrProviderNotConfigured) {
			t.Errorf("unknown-provider error must wrap ErrProviderNotConfigured (errors.Is classification), got %v", err)
		}
		if errors.Is(err, ErrUnavailable) {
			t.Errorf("a config gap is not a backend failure; it must not wrap the ErrUnavailable umbrella: %v", err)
		}
	})
}
