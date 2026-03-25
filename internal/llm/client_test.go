package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClientAuthHeader(t *testing.T) {
	tests := []struct {
		name       string
		apiKey     string
		wantAuth   bool
		wantBearer string
	}{
		{"no api key", "", false, ""},
		{"with api key", "sk-test-123", true, "Bearer sk-test-123"},
		{"ollama local", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAuth string
			var gotContentType string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				gotContentType = r.Header.Get("Content-Type")

				// Return a valid Ollama streaming response
				resp := map[string]any{
					"message": map[string]string{"role": "assistant", "content": "hi"},
					"done":    true,
				}
				json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			client := NewClient(server.URL, "test-model", tt.apiKey)
			_, err := client.Chat(context.Background(), []Message{
				{Role: "user", Content: "hello"},
			}, 0.7, 100, nil)
			if err != nil {
				t.Fatalf("Chat error: %v", err)
			}

			if gotContentType != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", gotContentType)
			}

			if tt.wantAuth {
				if gotAuth != tt.wantBearer {
					t.Errorf("Authorization = %q, want %q", gotAuth, tt.wantBearer)
				}
			} else {
				if gotAuth != "" {
					t.Errorf("Authorization should be empty, got %q", gotAuth)
				}
			}
		})
	}
}

func TestPingAuthHeader(t *testing.T) {
	tests := []struct {
		name     string
		apiKey   string
		wantAuth bool
	}{
		{"ping without key", "", false},
		{"ping with key", "sk-ping-test", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAuth string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				json.NewEncoder(w).Encode(map[string]any{
					"models": []map[string]string{{"name": "test-model"}},
				})
			}))
			defer server.Close()

			client := NewClient(server.URL, "test-model", tt.apiKey)
			err := client.Ping(context.Background())
			if err != nil {
				t.Fatalf("Ping error: %v", err)
			}

			if tt.wantAuth && gotAuth == "" {
				t.Error("expected Authorization header, got empty")
			}
			if !tt.wantAuth && gotAuth != "" {
				t.Errorf("expected no Authorization header, got %q", gotAuth)
			}
		})
	}
}

func TestChatStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens := []string{"Hello", " ", "world"}
		for _, tok := range tokens {
			resp := map[string]any{
				"message": map[string]string{"role": "assistant", "content": tok},
				"done":    false,
			}
			json.NewEncoder(w).Encode(resp)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{"role": "assistant", "content": ""},
			"done":    true,
		})
	}))
	defer server.Close()

	var tokens []string
	client := NewClient(server.URL, "test", "")
	result, err := client.Chat(context.Background(), []Message{
		{Role: "user", Content: "hi"},
	}, 0.7, 100, func(tok string) {
		tokens = append(tokens, tok)
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if result != "Hello world" {
		t.Errorf("result = %q, want 'Hello world'", result)
	}
	if len(tokens) != 3 {
		t.Errorf("got %d tokens, want 3", len(tokens))
	}
}

func TestHardwareRecommendation(t *testing.T) {
	tests := []struct {
		ramMB int
		want  string
	}{
		{8192, "llama3.1:8b"},
		{4096, "llama3.2:3b"},
		{2048, "phi3:mini"},
		{1024, "tinyllama"},
		{512, "tinyllama"},
	}
	for _, tt := range tests {
		got := HardwareRecommendation(tt.ramMB)
		if got != tt.want {
			t.Errorf("HardwareRecommendation(%d) = %q, want %q", tt.ramMB, got, tt.want)
		}
	}
}
