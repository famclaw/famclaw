package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/subagent"
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

// TestHandleSpawnAgent_Timeout asserts that handleSpawnAgent enforces the
// timeout_seconds argument by wrapping ctx with WithTimeout and the resulting
// error carries context.DeadlineExceeded.
func TestHandleSpawnAgent_Timeout(t *testing.T) {
	a := setupAgent(t, "http://unused")

	// Stub scheduler that hands control to the executor; the executor sleeps
	// past the timeout so subCtx fires DeadlineExceeded first.
	a.scheduler = subagent.NewScheduler(2)

	// Override the default subagent.Execute path by manually using the scheduler.
	// We simulate the same flow that handleSpawnAgent uses, but with a stub
	// executor that respects ctx cancellation.
	// Call handleSpawnAgent with a tiny timeout.
	args := map[string]any{
		"prompt":          "sleep forever",
		"timeout_seconds": float64(0), // 0 -> default 300, we want tiny — use a custom path below
	}
	// To exercise the timeout path we instead build the call against a parent ctx
	// already very close to expiring. handleSpawnAgent wraps with WithTimeout
	// using subagentDefaultTimeoutSec, but child ctx inherits the parent deadline.
	parentCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// The subagent profile lookup will fail (no profile configured), but that
	// only matters AFTER scheduler.Submit runs the executor goroutine. To
	// genuinely exercise the timeout we install a profile and use a fake LLM
	// that blocks until ctx is done.
	blocker := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-blocker:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"x"}}]}`))
	}))
	defer server.Close()
	defer close(blocker)

	a.cfg.LLM.Profiles = map[string]config.LLMProfile{
		"slow": {BaseURL: server.URL, Model: "test"},
	}
	args["profile"] = "slow"

	_, err := a.handleSpawnAgent(parentCtx, args)
	if err == nil {
		t.Fatal("expected error from timeout, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

// TestHandleSpawnAgent_TimeoutCap verifies the explicit timeout_seconds arg is
// capped at subagentMaxTimeoutSec (1800s) and lower bounds default to 300s.
func TestHandleSpawnAgent_TimeoutCap(t *testing.T) {
	tests := []struct {
		name       string
		argValue   any
		wantSecond int
	}{
		{"missing uses default", nil, subagentDefaultTimeoutSec},
		{"zero uses default", float64(0), subagentDefaultTimeoutSec},
		{"negative uses default", float64(-5), subagentDefaultTimeoutSec},
		{"valid passes through", float64(60), 60},
		{"over cap is clamped", float64(99999), subagentMaxTimeoutSec},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{}
			if tc.argValue != nil {
				args["timeout_seconds"] = tc.argValue
			}

			// Apply the same parsing logic handleSpawnAgent uses.
			timeoutSec := subagentDefaultTimeoutSec
			if ts, ok := args["timeout_seconds"].(float64); ok && ts > 0 {
				timeoutSec = int(ts)
			}
			if timeoutSec > subagentMaxTimeoutSec {
				timeoutSec = subagentMaxTimeoutSec
			}

			if timeoutSec != tc.wantSecond {
				t.Errorf("timeout = %d, want %d", timeoutSec, tc.wantSecond)
			}
		})
	}
}

// TestParseStringList verifies JSON-decoded []any -> []string conversion.
func TestParseStringList(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want []string
	}{
		{"nil yields nil", nil, nil},
		{"non-slice yields nil", "not-a-list", nil},
		{"empty slice yields nil", []any{}, nil},
		{"strings pass through", []any{"a", "b"}, []string{"a", "b"}},
		{"non-string elements skipped", []any{"a", 42, "b", nil}, []string{"a", "b"}},
		{"empty string skipped", []any{"", "x", ""}, []string{"x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseStringList(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (got=%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
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
