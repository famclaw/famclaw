package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/webfetch"
)

func TestSearch_EmptyQueryRejected(t *testing.T) {
	_, err := Search(context.Background(), "   ", Options{Endpoint: "http://example.com"})
	if err == nil {
		t.Fatalf("expected error for empty query, got nil")
	}
	if !strings.Contains(err.Error(), "empty query") {
		t.Errorf("expected error to contain 'empty query', got %v", err)
	}
}

func TestSearch_EmptyEndpointRejected(t *testing.T) {
	_, err := Search(context.Background(), "foo", Options{Endpoint: ""})
	if err == nil {
		t.Fatalf("expected error for empty endpoint, got nil")
	}
	if !strings.Contains(err.Error(), "endpoint not configured") {
		t.Errorf("expected error to contain 'endpoint not configured', got %v", err)
	}
}

// TestSearch_EndpointWithoutHostRejected verifies that an endpoint that
// parses but has no host (e.g. a bare path) is rejected with a clear
// configuration error, not silently turned into a relative request path.
func TestSearch_EndpointWithoutHostRejected(t *testing.T) {
	// A bare path parses successfully but has no host — this must be
	// rejected with a clear configuration error, not silently turned into
	// a relative request path.
	_, err := Search(context.Background(), "x", Options{Endpoint: "/search"})
	if err == nil {
		t.Fatal("expected error for endpoint without host, got nil")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("expected error to mention host, got %v", err)
	}
}

func TestSearch_HappyPath(t *testing.T) {
	const expectedQuery = "search term"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("expected path /search, got %s", r.URL.Path)
		}
		q := r.URL.Query()
		if got := q.Get("q"); got != expectedQuery {
			t.Errorf("expected q=%q, got %q", expectedQuery, got)
		}
		if got := q.Get("format"); got != "json" {
			t.Errorf("expected format=json, got %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"query": expectedQuery,
			"results": []map[string]string{
				{"title": "T1", "url": "https://e1", "content": "S1"},
				{"title": "T2", "url": "https://e2", "content": "S2"},
				{"title": "T3", "url": "https://e3", "content": "S3"},
			},
		})
	}))
	defer server.Close()

	hits, err := Search(context.Background(), expectedQuery, Options{
		Endpoint:             server.URL,
		MaxResults:           2,
		Timeout:              5 * time.Second,
		AllowPrivateNetworks: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	if hits[0].Title != "T1" || hits[0].URL != "https://e1" || hits[0].Content != "S1" {
		t.Errorf("hit[0] = %+v", hits[0])
	}
	if hits[1].Title != "T2" {
		t.Errorf("hit[1] = %+v", hits[1])
	}
}

func TestSearch_MaxResultsDefaultsAndCaps(t *testing.T) {
	type tc struct {
		max  int
		want int
	}
	tests := []tc{
		{max: -5, want: 8},
		{max: 0, want: 8},
		{max: 5, want: 5},
		{max: 20, want: 16},
	}
	mkServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			results := make([]map[string]string, 0, 16)
			for i := 1; i <= 16; i++ {
				results = append(results, map[string]string{
					"title":   fmt.Sprintf("T%d", i),
					"url":     fmt.Sprintf("https://e%d", i),
					"content": fmt.Sprintf("S%d", i),
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
		}))
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("max=%d", tt.max), func(t *testing.T) {
			server := mkServer()
			defer server.Close()
			hits, err := Search(context.Background(), "x", Options{
				Endpoint:             server.URL,
				MaxResults:           tt.max,
				Timeout:              5 * time.Second,
				AllowPrivateNetworks: true,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(hits) != tt.want {
				t.Fatalf("max=%d: expected %d hits, got %d", tt.max, tt.want, len(hits))
			}
		})
	}
}

func TestSearch_DefaultTimeoutIs30s(t *testing.T) {
	if defaultTimeout != 30*time.Second {
		t.Errorf("defaultTimeout = %v, want 30s", defaultTimeout)
	}
}

func TestSearch_DecodesGarbageError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()
	_, err := Search(context.Background(), "x", Options{
		Endpoint:             server.URL,
		Timeout:              5 * time.Second,
		AllowPrivateNetworks: true,
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("expected ErrUnavailable for garbage response, got %v", err)
	}
}

// TestSearch_BackendUnreachable_HTTPError verifies that a backend returning
// a non-2xx status produces ErrUnavailable — never an empty result set.
func TestSearch_BackendUnreachable_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := Search(context.Background(), "x", Options{
		Endpoint:             server.URL,
		Timeout:              5 * time.Second,
		AllowPrivateNetworks: true,
	})
	if err == nil {
		t.Fatal("expected error for 500 response, got nil — empty results would invite hallucination")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("expected ErrUnavailable, got %v", err)
	}
}

// TestSearch_BackendUnreachable_ConnectionRefused verifies that a backend
// that cannot be reached at all (connection refused) produces ErrUnavailable.
func TestSearch_BackendUnreachable_ConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot create listener: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // close so the connection is refused

	_, err = Search(context.Background(), "x", Options{
		Endpoint:             "http://" + addr,
		Timeout:              2 * time.Second,
		AllowPrivateNetworks: true,
	})
	if err == nil {
		t.Fatal("expected error for unreachable backend, got nil")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("expected ErrUnavailable, got %v", err)
	}
}

// TestSearch_ZeroResultsReturnsEmptySlice verifies that a backend returning
// zero hits is a normal "no results" outcome — NOT an error, and distinct
// from ErrUnavailable. This prevents the LLM from conflating "nothing found"
// with "search is broken."
func TestSearch_ZeroResultsReturnsEmptySlice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	hits, err := Search(context.Background(), "x", Options{
		Endpoint:             server.URL,
		Timeout:              5 * time.Second,
		AllowPrivateNetworks: true,
	})
	if err != nil {
		t.Fatalf("expected no error for zero results, got %v — zero results is a valid outcome, not an error", err)
	}
	if hits == nil {
		t.Fatal("expected non-nil (but empty) hit slice, got nil")
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits, got %d", len(hits))
	}
}

// TestSearch_RedirectToDisallowedHost verifies that a redirect to a host
// not on the URL allowlist is classified as a HostNotAllowedError (a
// configuration gap), NOT as ErrUnavailable. This confirms that the
// errors.As check in Search correctly traverses the error chain from
// webfetch.Fetch through the CheckRedirect wrapper.
func TestSearch_RedirectToDisallowedHost(t *testing.T) {
	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect to a host that is NOT in the allowlist.
		http.Redirect(w, r, "http://evil.example.com/search?q=test", http.StatusFound)
	}))
	defer redirectServer.Close()

	_, err := Search(context.Background(), "x", Options{
		Endpoint:             redirectServer.URL,
		Timeout:              5 * time.Second,
		AllowPrivateNetworks: true,
		HostValidator: func(host string) error {
			if host == "evil.example.com" {
				return webfetch.NewHostNotAllowedError(host)
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected error for redirect to disallowed host, got nil")
	}
	// The redirect-bounce host error must NOT be misclassified as
	// ErrUnavailable — it is a configuration error, not a backend outage.
	if errors.Is(err, ErrUnavailable) {
		t.Errorf("redirect to disallowed host should NOT be ErrUnavailable, got: %v", err)
	}
	// And it should be a HostNotAllowedError, confirming errors.As
	// traversed the CheckRedirect → url.Error → Fetch wrapping chain.
	var hostErr *webfetch.HostNotAllowedError
	if !errors.As(err, &hostErr) {
		t.Errorf("expected HostNotAllowedError for redirect to disallowed host, got: %v", err)
	}
}

// TestSearch_EndpointWithPath verifies that an endpoint containing a
// sub-path (e.g. http://host/searx) produces a probe/search URL of
// /searx/search, not /searx/search/search. This prevents the startup
// reachability check from always warning due to a double-path.
func TestSearch_EndpointWithPath(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	// Construct an endpoint with a sub-path.
	endpoint := server.URL + "/searx"
	_, err := Search(context.Background(), "x", Options{
		Endpoint:             endpoint,
		Timeout:              5 * time.Second,
		AllowPrivateNetworks: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantPath := "/searx/search"
	if capturedPath != wantPath {
		t.Errorf("path = %q, want %q (endpoint with sub-path should join, not double)", capturedPath, wantPath)
	}
}

func TestSearch_HostValidatorRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	_, err := Search(context.Background(), "x", Options{
		Endpoint:             server.URL,
		Timeout:              5 * time.Second,
		AllowPrivateNetworks: true,
		HostValidator: func(host string) error {
			return fmt.Errorf("host %q blocked by test", host)
		},
	})
	if err == nil {
		t.Fatalf("expected host-validator error, got nil")
	}
}

func TestFormatHits_Empty(t *testing.T) {
	if got := FormatHits(nil); got != "no results" {
		t.Errorf("nil: expected 'no results', got %q", got)
	}
	if got := FormatHits([]Hit{}); got != "no results" {
		t.Errorf("[]Hit{}: expected 'no results', got %q", got)
	}
}

func TestFormatHits_Numbered(t *testing.T) {
	hits := []Hit{
		{Title: "A", URL: "http://a", Content: "sa"},
		{Title: "B", URL: "http://b", Content: "sb"},
	}
	want := "1. A\n   http://a\n   sa\n\n2. B\n   http://b\n   sb"
	if got := FormatHits(hits); got != want {
		t.Errorf("FormatHits = %q\nwant %q", got, want)
	}
}

func TestFormatHits_SkipsEmptyFields(t *testing.T) {
	got := FormatHits([]Hit{{Title: "OnlyTitle"}})
	if got != "1. OnlyTitle" {
		t.Errorf("FormatHits = %q, want %q", got, "1. OnlyTitle")
	}
}
