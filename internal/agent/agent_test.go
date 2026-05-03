package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/subagent"
	"github.com/famclaw/famclaw/internal/webfetch"
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

	return NewAgent(user, cfg, client, ev, clf, db, AgentDeps{})
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

// TestHandleSpawnAgent_Timeout asserts that handleSpawnAgent enforces the
// timeout_seconds argument by wrapping ctx with WithTimeout and the resulting
// error carries context.DeadlineExceeded.
func TestHandleSpawnAgent_Timeout(t *testing.T) {
	a := setupAgent(t, "http://unused")
	a.scheduler = subagent.NewScheduler(2)

	// timeout_seconds=1 must be the deadline that fires, NOT the 5s parent ctx.
	// The elapsed-time assertion below distinguishes the two: if it took close
	// to 5s, the parent fired and the handler stopped honoring timeout_seconds.
	args := map[string]any{
		"prompt":          "sleep forever",
		"timeout_seconds": float64(1),
	}
	parentCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Fake LLM that blocks until its request ctx is canceled, so the only way
	// the call returns is when handleSpawnAgent's WithTimeout(ctx, 1s) fires.
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

	start := time.Now()
	_, err := a.handleSpawnAgent(parentCtx, args)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from timeout, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("timeout took %v — expected ~1s from timeout_seconds; parent ctx (5s) likely fired instead", elapsed)
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
		{"sub-second uses default (was: int truncation to 0)", float64(0.5), subagentDefaultTimeoutSec},
		{"just-under-1 uses default", float64(0.999), subagentDefaultTimeoutSec},
		{"exactly 1 passes through", float64(1), 1},
		{"fractional above 1 truncates to int", float64(60.7), 60},
		{"over cap is clamped", float64(99999), subagentMaxTimeoutSec},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{}
			if tc.argValue != nil {
				args["timeout_seconds"] = tc.argValue
			}

			got := normalizeTimeoutSeconds(args)
			if got != tc.wantSecond {
				t.Errorf("timeout = %d, want %d", got, tc.wantSecond)
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

func TestBuildMessages_DefaultUsesPromptBuilder(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{}, // SystemPrompt empty → builder path
		Users: []config.UserConfig{
			{Name: "julia", DisplayName: "Julia", Role: "child", AgeGroup: "age_8_12"},
		},
	}
	a := &Agent{cfg: cfg, user: &cfg.Users[0]}
	msgs := a.buildMessages(nil, "hi")
	if len(msgs) < 1 || msgs[0].Role != "system" {
		t.Fatalf("first message must be system, got %+v", msgs)
	}
	sys := msgs[0].Content
	for _, sub := range []string{"FamClaw", "Julia"} {
		if !strings.Contains(sys, sub) {
			t.Errorf("expected %q in system prompt: %q", sub, sys)
		}
	}
	if sys == "You are FamClaw, a helpful, friendly, and safe family AI assistant." {
		t.Error("agent is still emitting the legacy one-sentence prompt")
	}
}

func TestBuildMessages_OperatorOverrideKept(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{SystemPrompt: "You are a pirate."},
		Users: []config.UserConfig{
			{Name: "julia", DisplayName: "Julia", Role: "child", AgeGroup: "age_8_12"},
		},
	}
	a := &Agent{cfg: cfg, user: &cfg.Users[0]}
	msgs := a.buildMessages(nil, "hi")
	sys := msgs[0].Content
	if !strings.HasPrefix(sys, "You are a pirate.") {
		t.Errorf("operator override should be verbatim at start, got: %q", sys)
	}
}

func TestHandleWebFetch(t *testing.T) {
	newAgent := func(allowlist []string, fetcher func(context.Context, string, webfetch.Options) (*webfetch.Result, error)) *Agent {
		return &Agent{
			cfg: &config.Config{
				Tools: config.ToolsConfig{
					WebFetch: config.WebFetchConfig{
						Enabled:      true,
						URLAllowlist: allowlist,
						MaxBytes:     256 * 1024,
						TimeoutSec:   5,
					},
				},
			},
			webFetcher: fetcher,
		}
	}

	// stubFetcher records its calls and returns canned text.
	type call struct {
		rawURL string
		opts   webfetch.Options
	}

	tests := []struct {
		name        string
		allowlist   []string
		args        map[string]any
		fetcherErr  error
		fetcherRes  *webfetch.Result
		wantErrSub  string // empty = expect success
		wantOutSub  string
		wantCalled  bool
		wantMaxByte int64 // expected MaxBytes passed to fetcher (when called)
	}{
		{
			name:        "allowed host calls fetcher",
			allowlist:   []string{"example.com"},
			args:        map[string]any{"url": "https://example.com/x"},
			fetcherRes:  &webfetch.Result{URL: "https://example.com/x", StatusCode: 200, ContentType: "text/html", Text: "hello world"},
			wantOutSub:  "hello world",
			wantCalled:  true,
			wantMaxByte: 256 * 1024,
		},
		{
			name:       "disallowed host blocked, fetcher not called",
			allowlist:  []string{"never.example.com"},
			args:       map[string]any{"url": "https://other.example.com/x"},
			wantErrSub: "url_allowlist",
		},
		{
			name:       "subdomain of allowlisted host accepted",
			allowlist:  []string{"example.com"},
			args:       map[string]any{"url": "https://api.example.com/x"},
			fetcherRes: &webfetch.Result{URL: "https://api.example.com/x", StatusCode: 200, ContentType: "text/plain", Text: "ok"},
			wantOutSub: "ok",
			wantCalled: true,
		},
		{
			name:       "empty allowlist denied (SSRF guard)",
			allowlist:  nil,
			args:       map[string]any{"url": "https://example.com/x"},
			wantErrSub: "url_allowlist is empty",
		},
		{
			name:       "non-http scheme rejected before fetcher",
			allowlist:  []string{"example.com"},
			args:       map[string]any{"url": "file:///etc/passwd"},
			wantErrSub: "http(s)",
		},
		{
			name:       "javascript: URL rejected",
			allowlist:  []string{"example.com"},
			args:       map[string]any{"url": "javascript:alert(1)"},
			wantErrSub: "http(s)",
		},
		{
			name:       "missing url arg",
			allowlist:  []string{"example.com"},
			args:       map[string]any{},
			wantErrSub: "'url' argument",
		},
		{
			name:        "caller max_bytes only tightens",
			allowlist:   []string{"example.com"},
			args:        map[string]any{"url": "https://example.com/x", "max_bytes": float64(2)},
			fetcherRes:  &webfetch.Result{URL: "https://example.com/x", StatusCode: 200, ContentType: "text/plain", Truncated: true, Text: "he"},
			wantOutSub:  "Truncated: true",
			wantCalled:  true,
			wantMaxByte: 2,
		},
		{
			name:        "caller max_bytes cannot raise cap",
			allowlist:   []string{"example.com"},
			args:        map[string]any{"url": "https://example.com/x", "max_bytes": float64(10 * 1024 * 1024)},
			fetcherRes:  &webfetch.Result{URL: "https://example.com/x", StatusCode: 200, ContentType: "text/plain", Text: "ok"},
			wantOutSub:  "ok",
			wantCalled:  true,
			wantMaxByte: 256 * 1024,
		},
		{
			name:       "fetcher error propagates",
			allowlist:  []string{"example.com"},
			args:       map[string]any{"url": "https://example.com/x"},
			fetcherErr: fmt.Errorf("network down"),
			wantErrSub: "network down",
			wantCalled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *call
			fetcher := func(_ context.Context, rawURL string, opts webfetch.Options) (*webfetch.Result, error) {
				got = &call{rawURL: rawURL, opts: opts}
				if tt.fetcherErr != nil {
					return nil, tt.fetcherErr
				}
				return tt.fetcherRes, nil
			}
			a := newAgent(tt.allowlist, fetcher)

			out, err := a.handleWebFetch(context.Background(), tt.args)

			if tt.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (out=%q)", tt.wantErrSub, out)
				}
				if !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Errorf("err = %v, want substring %q", err, tt.wantErrSub)
				}
			} else if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}

			if tt.wantOutSub != "" && !strings.Contains(out, tt.wantOutSub) {
				t.Errorf("output missing %q: %q", tt.wantOutSub, out)
			}

			called := got != nil
			if called != tt.wantCalled {
				t.Errorf("fetcher called=%v, want=%v", called, tt.wantCalled)
			}
			if tt.wantCalled && tt.wantMaxByte > 0 && got.opts.MaxBytes != tt.wantMaxByte {
				t.Errorf("MaxBytes passed to fetcher = %d, want %d", got.opts.MaxBytes, tt.wantMaxByte)
			}
			if tt.wantCalled && got.opts.HostValidator == nil {
				t.Errorf("HostValidator should be set when fetcher is called")
			}
		})
	}
}

// TestHandleWebFetch_HostValidatorAppliesToRedirect ensures the validator
// closure handed to webfetch enforces the same allowlist on redirect
// targets as on the initial URL.
func TestHandleWebFetch_HostValidatorAppliesToRedirect(t *testing.T) {
	var capturedValidator func(string) error
	a := &Agent{
		cfg: &config.Config{
			Tools: config.ToolsConfig{
				WebFetch: config.WebFetchConfig{
					Enabled:      true,
					URLAllowlist: []string{"good.example"},
					MaxBytes:     1024,
					TimeoutSec:   5,
				},
			},
		},
		webFetcher: func(_ context.Context, _ string, opts webfetch.Options) (*webfetch.Result, error) {
			capturedValidator = opts.HostValidator
			return &webfetch.Result{StatusCode: 200, ContentType: "text/plain", Text: "ok"}, nil
		},
	}

	if _, err := a.handleWebFetch(context.Background(), map[string]any{"url": "https://good.example/page"}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if capturedValidator == nil {
		t.Fatal("HostValidator should have been passed to fetcher")
	}
	if err := capturedValidator("good.example"); err != nil {
		t.Errorf("good.example should pass validator, got: %v", err)
	}
	if err := capturedValidator("api.good.example"); err != nil {
		t.Errorf("api.good.example (subdomain) should pass validator, got: %v", err)
	}
	if err := capturedValidator("evil.example"); err == nil {
		t.Errorf("evil.example should be rejected by validator")
	}
}

