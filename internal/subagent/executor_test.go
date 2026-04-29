package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
)

// mockOpenAIResponse returns a handler that responds with the given content and tool calls.
func mockOpenAIResponse(content string, toolCalls []map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		msg := map[string]any{
			"role":    "assistant",
			"content": content,
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": msg, "finish_reason": "stop"},
			},
			"usage": map[string]any{
				"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func testConfig(serverURL string) *config.Config {
	return &config.Config{
		LLM: config.LLMConfig{
			Profiles: map[string]config.LLMProfile{
				"test-local": {
					BaseURL: serverURL,
					Model:   "test-model",
				},
			},
		},
	}
}

func TestExecute_NoTools(t *testing.T) {
	server := httptest.NewServer(mockOpenAIResponse("Hello from subagent", nil))
	defer server.Close()

	cfg := Config{Prompt: "say hello", LLMProfile: "test-local", MaxTurns: 5}
	deps := ExecutorDeps{
		Config:    testConfig(server.URL),
		MaxTokens: 100,
	}

	output, err := Execute(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if output != "Hello from subagent" {
		t.Errorf("output = %q, want %q", output, "Hello from subagent")
	}
}

func TestExecute_MaxTurnsExhausted(t *testing.T) {
	// Server always returns a tool call, never a final response.
	// The executor calls the LLM, gets a tool call, executes it (error: unknown tool),
	// re-calls the LLM with the error, gets another tool call, etc.
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"role":    "assistant",
						"content": "",
						"tool_calls": []map[string]any{
							{
								"id":   fmt.Sprintf("call_%d", callCount),
								"type": "function",
								"function": map[string]any{
									"name":      "nonexistent_tool",
									"arguments": map[string]any{"key": "val"},
								},
							},
						},
					},
					"finish_reason": "tool_calls",
				},
			},
			"usage": map[string]any{
				"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := Config{Prompt: "loop forever", LLMProfile: "test-local", MaxTurns: 3}
	deps := ExecutorDeps{
		Config:    testConfig(server.URL),
		MaxTokens: 100,
	}

	_, err := Execute(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected error for exhausted turns, got nil")
	}
	// MaxTurns=3 means 3 loop iterations, each with one LLM call after tool errors
	if callCount < cfg.MaxTurns {
		t.Errorf("expected at least %d LLM calls, got %d", cfg.MaxTurns, callCount)
	}
}

func TestExecute_UnknownProfile(t *testing.T) {
	cfg := Config{Prompt: "test", LLMProfile: "nonexistent-profile"}
	deps := ExecutorDeps{
		Config: &config.Config{
			LLM: config.LLMConfig{
				Profiles: map[string]config.LLMProfile{},
			},
		},
		MaxTokens: 100,
	}

	_, err := Execute(context.Background(), cfg, deps)
	if err == nil {
		t.Fatal("expected error for unknown profile, got nil")
	}
}
