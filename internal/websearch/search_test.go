package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		t.Fatalf("expected decode error, got nil")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("expected error to contain 'decode', got %v", err)
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
