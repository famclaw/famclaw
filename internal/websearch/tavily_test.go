package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestTavily_HappyPath (Slice 1): the adapter POSTs {query, max_results} with
// the Bearer key to the Tavily search endpoint and maps the response results
// to the shared Hit shape, capped at max_results.
//
// Auth scheme (verified against the Tavily API reference, docs.tavily.com,
// 2026-10): the key travels in the "Authorization: Bearer ***" header, NOT in
// the legacy "api_key" body field — so the test asserts the header is present
// and that the body does NOT carry an api_key.
func TestTavily_HappyPath(t *testing.T) {
	const key = "tvly-fake-key-123"
	var (
		captured     *http.Request
		capturedBody []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"T1","url":"https://e1","content":"S1"},
			{"title":"T2","url":"https://e2","content":"S2"},
			{"title":"T3","url":"https://e3","content":"S3"}]}`))
	}))
	defer srv.Close()

	p := NewTavilyProvider(TavilyConfig{APIKey: key, Endpoint: srv.URL, Timeout: 5 * time.Second})
	hits, err := p.Search(context.Background(), "search term", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits (max_results=2), got %d", len(hits))
	}
	if hits[0].Title != "T1" || hits[0].URL != "https://e1" || hits[0].Content != "S1" {
		t.Errorf("hit[0] = %+v", hits[0])
	}
	if hits[1].Title != "T2" || hits[1].URL != "https://e2" {
		t.Errorf("hit[1] = %+v", hits[1])
	}

	if captured == nil {
		t.Fatal("the test server did not receive the request")
	}
	if got := captured.Method; got != http.MethodPost {
		t.Errorf("method = %q, want POST", got)
	}
	if got := captured.Header.Get("Authorization"); got != "Bearer "+key {
		t.Errorf("Authorization header = %q, want Bearer <key>", got)
	}
	if got := captured.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var bodyReq struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
		APIKey     string `json:"api_key"`
	}
	if jerr := json.Unmarshal(capturedBody, &bodyReq); jerr != nil {
		t.Fatalf("request body is not JSON: %v", jerr)
	}
	if bodyReq.Query != "search term" {
		t.Errorf("body query = %q, want %q", bodyReq.Query, "search term")
	}
	if bodyReq.MaxResults != 2 {
		t.Errorf("body max_results = %d, want 2", bodyReq.MaxResults)
	}
	if bodyReq.APIKey != "" {
		t.Errorf("api_key must NOT be sent in the body (the Bearer header is the auth scheme), got %q", bodyReq.APIKey)
	}
}

// TestTavily_MaxResultsDefaultsAndCaps verifies the per-request max_results
// sent to Tavily: 0/negative → the package default, and a large limit is
// capped at the hard max.
func TestTavily_MaxResultsDefaultsAndCaps(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{name: "zero defaults", limit: 0, want: defaultMaxResults},
		{name: "negative defaults", limit: -3, want: defaultMaxResults},
		{name: "explicit under cap", limit: 5, want: 5},
		{name: "large capped at hard max", limit: 50, want: hardMaxResults},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"results":[]}`))
			}))
			defer srv.Close()

			p := NewTavilyProvider(TavilyConfig{APIKey: "k", Endpoint: srv.URL, Timeout: 5 * time.Second})
			if _, err := p.Search(context.Background(), "x", tt.limit); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var bodyReq struct {
				MaxResults int `json:"max_results"`
			}
			if jerr := json.Unmarshal(capturedBody, &bodyReq); jerr != nil {
				t.Fatalf("request body is not JSON: %v", jerr)
			}
			if bodyReq.MaxResults != tt.want {
				t.Errorf("max_results = %d, want %d", bodyReq.MaxResults, tt.want)
			}
		})
	}
}

// TestTavily_EmptyQueryRejected: a blank query is rejected before any request.
func TestTavily_EmptyQueryRejected(t *testing.T) {
	p := NewTavilyProvider(TavilyConfig{APIKey: "k", Endpoint: "http://127.0.0.1:1", Timeout: 5 * time.Second})
	_, err := p.Search(context.Background(), "   ", 5)
	if err == nil {
		t.Fatal("expected an error for a blank query")
	}
	if !strings.Contains(err.Error(), "empty query") {
		t.Errorf("error should mention the empty query, got %v", err)
	}
}

// TestTavily_NoKey: a provider built with an empty key fails before dialing,
// as an honest ErrUnavailable (the specific auth sentinel is a later slice).
func TestTavily_NoKey(t *testing.T) {
	p := NewTavilyProvider(TavilyConfig{APIKey: "", Endpoint: "http://127.0.0.1:1", Timeout: 5 * time.Second})
	_, err := p.Search(context.Background(), "x", 5)
	if err == nil {
		t.Fatal("expected an error when the provider has no API key")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("expected ErrUnavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "no API key") {
		t.Errorf("error should mention the missing key, got %v", err)
	}
}

// TestTavily_ErrorClasses verifies the Slice 1 error contract at the
// umbrella level: every failure maps to ErrUnavailable (the specific
// auth/rate/5xx/malformed sentinels are introduced by the later
// error-classes slice), while a valid zero-result response is a success
// (nil error + empty hit slice) — never conflated with a failure.
func TestTavily_ErrorClasses(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantErr   bool
		wantEmpty bool // valid empty result: nil error, empty (non-nil) hits
	}{
		{name: "401 auth failure", status: 401, body: `{"detail":"unauthorized"}`, wantErr: true},
		{name: "429 rate limited", status: 429, body: `{"detail":"rate limited"}`, wantErr: true},
		{name: "500 server error", status: 500, body: `oops`, wantErr: true},
		{name: "503 unavailable", status: 503, body: ``, wantErr: true},
		{name: "200 non-JSON body", status: 200, body: `this is not json`, wantErr: true},
		{name: "200 valid empty result", status: 200, body: `{"results":[]}`, wantEmpty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			p := NewTavilyProvider(TavilyConfig{APIKey: "k", Endpoint: srv.URL, Timeout: 5 * time.Second})
			hits, err := p.Search(context.Background(), "x", 5)

			switch {
			case tt.wantErr:
				if err == nil {
					t.Fatalf("expected an error, got nil (hits=%v)", hits)
				}
				if !errors.Is(err, ErrUnavailable) {
					t.Errorf("expected the ErrUnavailable umbrella, got %v", err)
				}
			case tt.wantEmpty:
				if err != nil {
					t.Fatalf("a valid empty result must not be an error, got %v", err)
				}
				if hits == nil || len(hits) != 0 {
					t.Errorf("expected an empty (non-nil) hit slice, got %v", hits)
				}
			}
		})
	}
}

// TestTavily_SecretRedaction: on an auth failure the provider must not echo
// the response body (which on 401 can reference key-derived details) into the
// returned error — the key must never leak into a message the LLM would relay.
func TestTavily_SecretRedaction(t *testing.T) {
	const key = "tvly-fake-secret-0000000000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"invalid key ` + key + ` was rejected"}`))
	}))
	defer srv.Close()

	p := NewTavilyProvider(TavilyConfig{APIKey: key, Endpoint: srv.URL, Timeout: 5 * time.Second})
	_, err := p.Search(context.Background(), "x", 5)
	if err == nil {
		t.Fatal("expected an error on 401")
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("the error must not contain the API key: %v", err)
	}
	if strings.Contains(err.Error(), "invalid key") {
		t.Errorf("the error must not echo the provider response body: %v", err)
	}
}

// TestTavily_RedirectToDisallowedScheme: the guarded transport refuses to
// follow a redirect to a non-http(s) scheme (a redirect is a fresh, untrusted
// destination).
func TestTavily_RedirectToDisallowedScheme(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "gopher://example.com", http.StatusFound)
	}))
	defer srv.Close()

	p := NewTavilyProvider(TavilyConfig{APIKey: "k", Endpoint: srv.URL, Timeout: 5 * time.Second})
	_, err := p.Search(context.Background(), "x", 5)
	if err == nil {
		t.Fatal("expected an error when the API redirects to a disallowed scheme")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("expected ErrUnavailable, got %v", err)
	}
}
