package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDetectContextWindowOllama(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			json.NewEncoder(w).Encode(map[string]any{
				"model_info": map[string]any{
					"context_length": 8192,
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	n := DetectContextWindow(context.Background(), server.URL, "test", "")
	if n != 8192 {
		t.Errorf("DetectContextWindow = %d, want 8192", n)
	}
}

func TestDetectContextWindowLlamaCpp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			json.NewEncoder(w).Encode(map[string]any{
				"default_generation_settings": map[string]any{
					"n_ctx": 32768,
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	n := DetectContextWindow(context.Background(), server.URL, "unknown-model", "")
	if n != 32768 {
		t.Errorf("DetectContextWindow = %d, want 32768", n)
	}
}

func TestDetectContextWindowKnownModel(t *testing.T) {
	// No server — just model lookup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	n := DetectContextWindow(context.Background(), server.URL, "gpt-4o-mini", "")
	if n != 128000 {
		t.Errorf("DetectContextWindow(gpt-4o-mini) = %d, want 128000", n)
	}
}

func TestDetectContextWindowDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	n := DetectContextWindow(context.Background(), server.URL, "totally-unknown-model", "")
	if n != 4096 {
		t.Errorf("DetectContextWindow(unknown) = %d, want 4096", n)
	}
}

func TestKnownContextWindows(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"gpt-4o", 128000},
		{"llama-3.3-70b-versatile", 128000},
		{"gemma4:e2b", 131072},
		{"tinyllama", 2048},
		{"phi4-mini", 16384},
	}

	for _, tt := range tests {
		if got, ok := knownContextWindows[tt.model]; !ok || got != tt.want {
			t.Errorf("knownContextWindows[%q] = %d, want %d", tt.model, got, tt.want)
		}
	}
}
