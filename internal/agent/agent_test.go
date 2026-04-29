package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

func setupAgent(t *testing.T, serverURL string) *Agent {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// Policies are embedded in the binary.
	ev, err := policy.NewEvaluator("", "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		LLM: config.LLMConfig{
			BaseURL:           serverURL,
			Model:             "test",
			Temperature:       0.7,
			MaxResponseTokens: 100,
		},
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent"},
		},
	}

	user := &cfg.Users[0]
	client := llm.NewClient(serverURL, "test", "")
	clf := classifier.New()

	return NewAgent(user, cfg, client, ev, clf, db)
}

func mockLLMServer(t *testing.T, messages []llm.Message) *httptest.Server {
	t.Helper()
	callIdx := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "test"}}})
			return
		}

		var req struct {
			Stream bool `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		if callIdx >= len(messages) {
			callIdx = len(messages) - 1
		}
		msg := messages[callIdx]
		callIdx++

		if req.Stream {
			// SSE streaming response
			w.Header().Set("Content-Type", "text/event-stream")
			chunk := map[string]any{
				"choices": []map[string]any{{
					"delta": map[string]any{"content": msg.Content},
				}},
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
			fmt.Fprint(w, "data: [DONE]\n\n")
		} else {
			// Non-streaming response
			resp := map[string]any{
				"choices": []map[string]any{{
					"message":       msg,
					"finish_reason": "stop",
				}},
			}
			json.NewEncoder(w).Encode(resp)
		}
	}))
}

func TestAgentChatNoToolCalls(t *testing.T) {
	server := mockLLMServer(t, []llm.Message{
		{Role: "assistant", Content: "Hello!"},
	})
	defer server.Close()

	agent := setupAgent(t, server.URL)

	resp, err := agent.Chat(context.Background(), "hi", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "Hello!" {
		t.Errorf("content = %q, want Hello!", resp.Content)
	}
	if resp.PolicyAction != "allow" {
		t.Errorf("action = %q, want allow", resp.PolicyAction)
	}
}

func TestAgentChatPoolNil(t *testing.T) {
	// Even with tool_calls in response, if pool is nil, they're ignored
	server := mockLLMServer(t, []llm.Message{
		{
			Role: "assistant", Content: "Let me check...",
			ToolCalls: []llm.ToolCall{
				{Function: llm.ToolCallFunction{Name: "echo", Arguments: map[string]any{"text": "hi"}}},
			},
		},
	})
	defer server.Close()

	agent := setupAgent(t, server.URL)
	// pool is nil — tool calls should be skipped

	resp, err := agent.Chat(context.Background(), "hello", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "Let me check..." {
		t.Errorf("content = %q", resp.Content)
	}
}

func TestAgentSetPool(t *testing.T) {
	agent := &Agent{}
	if agent.pool != nil {
		t.Error("pool should be nil by default")
	}
	agent.SetPool(nil)
	if agent.pool != nil {
		t.Error("SetPool(nil) should keep pool nil")
	}
}

func TestAgentChatMessageTypes(t *testing.T) {
	// Test that LLM tool call types serialize correctly
	tc := llm.ToolCall{
		Function: llm.ToolCallFunction{
			Name:      "test_tool",
			Arguments: map[string]any{"key": "value"},
		},
	}
	data, err := json.Marshal(tc)
	if err != nil {
		t.Fatal(err)
	}

	var decoded llm.ToolCall
	json.Unmarshal(data, &decoded)
	if decoded.Function.Name != "test_tool" {
		t.Errorf("name = %q", decoded.Function.Name)
	}
}

func TestAgentChatMessageWithToolCalls(t *testing.T) {
	msg := llm.Message{
		Role:    "assistant",
		Content: "Calling tool...",
		ToolCalls: []llm.ToolCall{
			{Function: llm.ToolCallFunction{Name: "echo", Arguments: map[string]any{"text": "hi"}}},
		},
	}
	data, _ := json.Marshal(msg)
	var decoded llm.Message
	json.Unmarshal(data, &decoded)

	if len(decoded.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(decoded.ToolCalls))
	}
	if decoded.ToolCalls[0].Function.Name != "echo" {
		t.Errorf("tool name = %q", decoded.ToolCalls[0].Function.Name)
	}
}

func TestFilterOutput(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		blocked bool
	}{
		{"safe response", "The sun is a star at the center of our solar system.", false},
		{"math help", "To solve 2x + 3 = 7, subtract 3 from both sides.", false},
		{"contains suicide", "Here's how to commit suicide...", true},
		{"contains porn", "Let me describe pornography...", true},
		{"contains slur", "Here's a racial slur you can use...", true},
		{"contains bomb", "Here's how to make a bomb...", true},
		{"case insensitive", "SUICIDE is a serious topic.", true},
		{"partial match", "The movie had explicit content warnings.", true},
		{"safe mention of topic", "If you need help, call a crisis hotline.", false},
		{"empty response", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterOutput(tt.text)
			if got != tt.blocked {
				t.Errorf("filterOutput(%q) = %v, want %v", tt.text[:min(50, len(tt.text))], got, tt.blocked)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
