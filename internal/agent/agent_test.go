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

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/filetool"
	"github.com/famclaw/famclaw/internal/familystate"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/subagent"
	"github.com/famclaw/famclaw/internal/todo"
	"github.com/famclaw/famclaw/internal/usermemory"
	"github.com/famclaw/famclaw/internal/webfetch"
	"github.com/famclaw/famclaw/internal/websearch"
	"github.com/famclaw/famclaw/internal/compress"
	"github.com/famclaw/famclaw/internal/skillbridge"
)

func setupAgent(t *testing.T, serverURL string) *Agent {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// Policies are embedded in the binary.
	ev, err := policy.NewEvaluator("", "", "")
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
	msgs := a.buildMessages(context.Background(), nil, "hi")
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
	msgs := a.buildMessages(context.Background(), nil, "hi")
	sys := msgs[0].Content
	if !strings.HasPrefix(sys, "You are a pirate.") {
		t.Errorf("operator override should be verbatim at start, got: %q", sys)
	}
}

func TestHandleWebFetch(t *testing.T) {
	newAgent := func(allowlist []string, fetcher func(context.Context, string, webfetch.Options) (*webfetch.Result, error)) *Agent {
		return &Agent{
			user: &config.UserConfig{Name: "testuser", Role: "parent"},
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
		user: &config.UserConfig{Name: "testuser", Role: "parent"},
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

func TestHandleGetFamilyState(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	fs := familystate.NewStore(db)
	ctx := context.Background()
	for _, f := range []familystate.Fact{
		{Category: "pets", Subject: "family", Label: "Stella", Value: "cat, age 5", CreatedBy: "dep"},
		{Category: "pets", Subject: "family", Label: "Rex", Value: "dog, age 3", CreatedBy: "dep"},
		{Category: "allergies", Subject: "teo", Label: "peanuts", Value: "severe", CreatedBy: "dep"},
		// Orphan — should be filtered out of the rendered output.
		{Category: "pets", Subject: "ghost", Label: "x", Value: "y", CreatedBy: "dep"},
	} {
		f := f
		if err := fs.UpsertFact(ctx, &f); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	cfg := &config.Config{Users: []config.UserConfig{
		{Name: "dep", Role: "parent"},
		{Name: "teo", Role: "child", AgeGroup: "age_13_17"},
	}}
	a := &Agent{cfg: cfg, familyState: fs, user: &cfg.Users[0]}

	cases := []struct {
		name      string
		args      map[string]any
		wantSub   []string // substrings the output must contain
		wantNoSub []string // substrings the output must NOT contain
	}{
		{
			name:      "category filter returns only pets, orphan excluded",
			args:      map[string]any{"category": "pets"},
			wantSub:   []string{"Stella", "Rex"},
			wantNoSub: []string{"peanuts", "ghost"},
		},
		{
			name:    "no filter returns every category",
			args:    map[string]any{},
			wantSub: []string{"Stella", "peanuts"},
		},
		{
			name:    "unknown category returns explanatory string, no error",
			args:    map[string]any{"category": "nope"},
			wantSub: []string{"No facts in category"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := a.handleGetFamilyState(ctx, tc.args)
			if err != nil {
				t.Fatalf("handle: %v", err)
			}
			for _, s := range tc.wantSub {
				if !strings.Contains(out, s) {
					t.Errorf("output missing %q:\n%s", s, out)
				}
			}
			for _, s := range tc.wantNoSub {
				if strings.Contains(out, s) {
					t.Errorf("output contains unwanted %q:\n%s", s, out)
				}
			}
		})
	}
}

func TestHandleGetFamilyState_NoStoreDegrades(t *testing.T) {
	a := &Agent{cfg: &config.Config{}, familyState: nil, user: &config.UserConfig{Name: "x", Role: "parent"}}
	out, err := a.handleGetFamilyState(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "not configured") {
		t.Errorf("nil-store path should return graceful string, got %q", out)
	}
}

func TestHandleProposeFamilyFact_Parent_AutoApply(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fs := familystate.NewStore(db)

	cfg := &config.Config{Users: []config.UserConfig{
		{Name: "dep", Role: "parent"},
		{Name: "teo", Role: "child", AgeGroup: "age_13_17"},
	}}

	// Real evaluator so the OPA gate fires (uses embedded policies).
	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatalf("evaluator: %v", err)
	}

	a := &Agent{cfg: cfg, db: db, familyState: fs, evaluator: ev, user: &cfg.Users[0], gateway: "test"}

	out, err := a.handleProposeFamilyFact(context.Background(), map[string]any{
		"category": "pets", "subject": "family", "label": "Stella", "value": "cat",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if !strings.Contains(out, "auto-apply") {
		t.Errorf("expected auto-apply message, got %q", out)
	}

	got, err := fs.ListFacts(context.Background(), familystate.FilterOpts{Category: "pets"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Label != "Stella" {
		t.Errorf("fact not applied: %+v", got)
	}
}

func TestHandleProposeFamilyFact_Child_QueuesApproval(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fs := familystate.NewStore(db)

	cfg := &config.Config{Users: []config.UserConfig{
		{Name: "dep", Role: "parent"},
		{Name: "teo", DisplayName: "Teo", Role: "child", AgeGroup: "age_13_17"},
	}}
	a := &Agent{cfg: cfg, db: db, familyState: fs, user: &cfg.Users[1], gateway: "test"}

	out, err := a.handleProposeFamilyFact(context.Background(), map[string]any{
		"category": "pets", "subject": "family", "label": "Rex", "value": "dog",
		"reason": "found a stray",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if !strings.Contains(out, "Proposal sent") {
		t.Errorf("expected proposal message, got %q", out)
	}

	// Fact should NOT exist yet.
	got, _ := fs.ListFacts(context.Background(), familystate.FilterOpts{Category: "pets"})
	if len(got) != 0 {
		t.Errorf("child proposal should not have applied fact: %+v", got)
	}
	// An approval row should exist with the proposal envelope.
	pending, err := db.PendingApprovals(context.Background())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Category != familystate.ProposalKind {
		t.Errorf("expected one ProposalKind approval, got %+v", pending)
	}
	p, err := familystate.DecodeProposal([]byte(pending[0].QueryText))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Label != "Rex" || p.ProposedBy != "teo" {
		t.Errorf("decoded proposal wrong: %+v", p)
	}
}

func TestHandleProposeFamilyFact_Rejects(t *testing.T) {
	a := &Agent{cfg: &config.Config{Users: []config.UserConfig{{Name: "dep", Role: "parent"}}}, familyState: &familystate.Store{}, user: &config.UserConfig{Name: "dep", Role: "parent"}}

	cases := []struct {
		name string
		args map[string]any
		want string // substring expected in returned string
	}{
		{name: "unknown subject", args: map[string]any{"category": "pets", "subject": "ghost", "label": "x", "value": "y"}, want: "not a family member"},
		{name: "label too long", args: map[string]any{"category": "pets", "subject": "family", "label": strings.Repeat("L", 65), "value": "y"}, want: "label too long"},
		{name: "value too long", args: map[string]any{"category": "pets", "subject": "family", "label": "x", "value": strings.Repeat("V", 513)}, want: "value too long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := a.handleProposeFamilyFact(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("out=%q want substring %q", out, tc.want)
			}
		})
	}
}

func TestIsSubagentExcludedTool(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "builtin__spawn_agent", want: true},
		{name: "builtin__set_family_fact", want: true},
		{name: "builtin__delete_family_fact", want: true},
		{name: "builtin__add_family_category", want: true},
		{name: "builtin__delete_family_category", want: true},
		{name: "builtin__propose_family_fact", want: true},
		{name: "builtin__approve_request", want: true},
		{name: "builtin__deny_request", want: true},
		{name: "builtin__set_user_role", want: true},
		{name: "builtin__link_account", want: true},
		// Read tools and unprivileged calls are NOT excluded.
		{name: "builtin__web_fetch", want: false},
		{name: "builtin__tool_result_more", want: false},
		{name: "builtin__get_family_state", want: false},
		{name: "builtin__list_pending_approvals", want: false}, // read-only
		{name: "mcp__weather__forecast", want: false},
		{name: "", want: false},
	}
	for _, tc := range cases {
		if got := isSubagentExcludedTool(tc.name); got != tc.want {
			t.Errorf("isSubagentExcludedTool(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestStreamedOutputGate verifies that blocked streamed tokens never reach
// the onToken callback — this is the regression for the streamed-output-
// gate bypass where the LLM streamed tokens directly to the gateway before
// EvaluateAndApply could filter them.
func TestStreamedOutputGate(t *testing.T) {
	tests := []struct {
		name          string
		userRole      string
		userAgeGroup  string
		llmResponse   string
		expectBlocked bool // output gate blocked (no tokens emitted, safe content)
		expectRedact  bool // soft-blocked: tokens emitted but content redacted
		expectSafeMsg bool // blocked: content replaced with safe fallback
		expectTokens  int  // 0 = blocked, >0 = allowed/soft-blocked (at least one token emitted)
	}{
		{
			name:          "hard-blocked self_harm child",
			userRole:      "child",
			userAgeGroup:  "age_8_12",
			llmResponse:   "I don't want to live anymore, I want to suicide",
			expectBlocked: true,
			expectSafeMsg: true,
			expectTokens:  0,
		},
		{
			name:          "hard-blocked violence parent",
			userRole:      "parent",
			userAgeGroup:  "adult",
			llmResponse:   "Here is how to make a bomb and kill yourself",
			expectBlocked: true,
			expectSafeMsg: true,
			expectTokens:  0,
		},
		{
			name:          "hard-blocked extreme_content",
			userRole:      "child",
			userAgeGroup:  "under_8",
			llmResponse:   "That involves pornography and explicit content",
			expectBlocked: true,
			expectSafeMsg: true,
			expectTokens:  0,
		},
		{
			name:          "hard-blocked illegal_harm",
			userRole:      "child",
			userAgeGroup:  "age_13_17",
			llmResponse:   "How to steal and how to hack a computer",
			expectBlocked: true,
			expectSafeMsg: true,
			expectTokens:  0,
		},
		{
			name:          "allowed_benign_child",
			userRole:      "child",
			userAgeGroup:  "age_8_12",
			llmResponse:   "Hello there, how can I help?",
			expectBlocked: false,
			expectTokens:  1,
		},
		{
			name:          "allowed_benign_parent",
			userRole:      "parent",
			userAgeGroup:  "adult",
			llmResponse:   "Sure, I can help with that.",
			expectBlocked: false,
		},
		{
			name:          "soft_blocked_child_violence",
			userRole:      "child",
			userAgeGroup:  "under_8",
			llmResponse:   "The movie had a lot of violence and death",
			expectBlocked: false,
			expectRedact:  true,
			expectTokens:  1,
		},
		{
			name:          "allowed_parent_soft_blocked_passes",
			userRole:      "parent",
			userAgeGroup:  "adult",
			llmResponse:   "The movie had a lot of violence and death",
			expectBlocked: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Collect every token the callback receives.
			var tokens []string
			onToken := func(tok string) {
				tokens = append(tokens, tok)
			}

			// LLM returns the test response.
			server := mockLLMServer(t, []llm.Message{
				{Role: "assistant", Content: tt.llmResponse},
			})
			defer server.Close()

			db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			ev, err := policy.NewEvaluator("", "", "")
			if err != nil {
				t.Fatal(err)
			}

			cfg := &config.Config{
				LLM: config.LLMConfig{
					BaseURL:           server.URL,
					Model:             "test",
					Temperature:       0.7,
					MaxResponseTokens: 100,
				},
				Users: []config.UserConfig{
					{Name: "user1", DisplayName: "User", Role: tt.userRole, AgeGroup: tt.userAgeGroup},
				},
			}
			user := &cfg.Users[0]
			client := llm.NewClient(server.URL, "test", "")
			clf := classifier.New()

			agent := NewAgent(user, cfg, client, ev, clf, db, AgentDeps{})

			resp, err := agent.Chat(context.Background(), "hello", onToken)
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}

			if tt.expectBlocked {
				// Hard-blocked: no tokens should ever reach onToken.
				if len(tokens) > 0 {
					t.Errorf("onToken was called %d times — streamed tokens leaked before the gate", len(tokens))
					for i, tok := range tokens {
						t.Errorf("  token[%d] = %q", i, tok)
					}
				}
				if tt.expectSafeMsg {
					// Blocked responses get a safe fallback message.
					if !strings.Contains(resp.Content, "unable to send") {
						t.Errorf("Content = %q, expected safe fallback for blocked output", resp.Content)
					}
				}
			} else {
				// Not blocked: tokens must have been emitted after the gate.
				if tt.expectTokens > 0 && len(tokens) == 0 {
					t.Fatal("onToken was never called — tokens were not emitted for an allowed response")
				}
				if tt.expectRedact {
					// Soft-blocked: content should have redacted keywords.
					if !strings.Contains(resp.Content, "[redacted]") {
						t.Errorf("Content = %q, expected redacted keywords for soft-blocked output", resp.Content)
					}
				}
			}
		})
	}
}

// mockToolChatter is a Chatter that returns a tool call on the first
// ChatWithTools call and a final response on the second call.
type mockToolChatter struct {
	callCount int
}

func (m *mockToolChatter) Chat(_ context.Context, _ []llm.Message, _ float64, _ int, _ func(string)) (string, error) {
	return "unexpected Chat call", errors.New("Chat should not be called when tools are present")
}

func (m *mockToolChatter) ChatMessage(_ context.Context, _ []llm.Message, _ float64, _ int) (*llm.Message, error) {
	return nil, errors.New("ChatMessage should not be called")
}

func (m *mockToolChatter) ChatSync(_ context.Context, _ []llm.Message, _ float64, _ int) (string, error) {
	return "unexpected ChatSync call", errors.New("ChatSync should not be called")
}

func (m *mockToolChatter) ChatWithTools(_ context.Context, _ []llm.Message, _ float64, _ int, _ []llm.ToolDef) (*llm.Message, error) {
	m.callCount++
	if m.callCount == 1 {
		// First call: return a tool call for builtin__get_family_state
		return &llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:       "call_1",
				Function: llm.ToolCallFunction{Name: "builtin__get_family_state", Arguments: map[string]any{}},
			}},
		}, nil
	}
	// Second call: return final response
	return &llm.Message{Role: "assistant", Content: "family state retrieved"}, nil
}

// TestToolCallDrainEmptyBufferedTokens verifies that the drain logic handles
// the tool-call path correctly: ChatWithTools does not invoke OnToken, so
// bufferedTokens remains empty and the drain block at agent.go:307 is skipped.
// This is the regression guard for the review-5 finding.
func TestToolCallDrainEmptyBufferedTokens(t *testing.T) {
	t.Parallel()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		LLM: config.LLMConfig{
			Temperature:       0.7,
			MaxResponseTokens: 100,
		},
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent"},
		},
	}
	user := &cfg.Users[0]
	clf := classifier.New()

	mock := &mockToolChatter{}
	agent := NewAgent(user, cfg, mock, ev, clf, db, AgentDeps{
		BuiltinTools: []agentcore.Tool{{
			Name:        "builtin__get_family_state",
			Description: "Get family state information",
			InputSchema: map[string]any{"type": "object"},
			Source:      "builtin",
		}},
	})

	var tokens []string
	onToken := func(tok string) {
		tokens = append(tokens, tok)
	}

	resp, err := agent.Chat(context.Background(), "say hello", onToken)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// The tool-call path uses ChatWithTools which does not stream tokens.
	// bufferedTokens stays empty, so the drain block is skipped.
	if len(tokens) > 0 {
		t.Errorf("onToken was called %d times — expected 0 for tool-call path", len(tokens))
	}

	// The final response should be the tool output.
	if !strings.Contains(resp.Content, "family state retrieved") {
		t.Errorf("Content = %q, expected tool output with 'family state retrieved'", resp.Content)
	}

	// Verify ChatWithTools was called (tool loop ran).
	if mock.callCount < 2 {
		t.Errorf("ChatWithTools was called %d times — expected at least 2 (tool call + final response)", mock.callCount)
	}
}

func TestBuildMessagesContextWindow(t *testing.T) {
	// Set a small context window to trigger compression.
	const maxContextTokens = 512
	cfg := &config.Config{
		LLM: config.LLMConfig{
			MaxContextTokens: maxContextTokens,
			MaxResponseTokens: 100,
			// SystemPrompt empty to use default.
			SystemPrompt: "",
		},
	}
	// Create a user config.
	user := &config.UserConfig{
		Name:   "testuser",
		AgeGroup: "age_8_12", // any group
	}
	// Create an agent with minimal dependencies.
	agent := &Agent{
		cfg:     cfg,
		user:    user,
		skills:  []*skillbridge.Skill{}, // empty
		builtinTools: []agentcore.Tool{}, // empty
		evaluator: nil, // nil to avoid nil pointer in skillbridge calls
		// Other fields are not used in buildMessages.
	}

	// Build a long history of alternating user and assistant messages.
	var history []*store.Message
	const numTurns = 100 // 100 turns => 200 messages
	for i := 0; i < numTurns; i++ {
		// User message
		history = append(history, &store.Message{
			Role:         "user",
			Content:      fmt.Sprintf("User message %d", i),
			PolicyAction: "allow",
		})
		// Assistant message
		history = append(history, &store.Message{
			Role:         "assistant",
			Content:      fmt.Sprintf("Assistant message %d", i),
			PolicyAction: "allow",
		})
	}
	// The last message in history is an assistant message.
	currentMessage := "Current user message"

	// Call buildMessages.
	msgs := agent.buildMessages(context.Background(), history, currentMessage)

	// Verify that the system prompt is present (first message).
	if len(msgs) == 0 {
		t.Fatalf("No messages returned")
	}
	if msgs[0].Role != "system" {
		t.Fatalf("First message should be system prompt, got %s", msgs[0].Role)
	}
	// Verify that the current user message is present (last message).
	if msgs[len(msgs)-1].Role != "user" {
		t.Fatalf("Last message should be user, got %s", msgs[len(msgs)-1].Role)
	}
	if msgs[len(msgs)-1].Content != currentMessage {
		t.Fatalf("Last message content mismatch: got %q, expected %q", msgs[len(msgs)-1].Content, currentMessage)
	}

	// Optionally, we can also check that the number of messages is reasonable (e.g., not too large).
	// Verify that the total tokens are within the budget.
	// Use the same estimator as the compress package (SimpleEstimator) and the same budget calculation.
	// We'll copy the logic from compress.Compress to compute the budget and total tokens.
	// Note: the compress package uses a margin of 0.15 if EstimatorMargin is zero.
	// We'll compute the budget as:
	//   budget = int(float64(effectiveContextWindow) * 0.85) * (1 - margin)
	//   margin = 0.15
	//   effectiveContextWindow = maxContextTokens - cfg.LLM.MaxResponseTokens (or maxContextTokens if the subtraction is non-positive)
	//   total tokens estimated with SimpleEstimator (chars/4) plus 4 tokens overhead per message.
	// We'll use the compress package's SimpleEstimator to be safe.
	est := &compress.SimpleEstimator{}
	// Compute total tokens of the returned messages.
	total := 0
	for _, m := range msgs {
		total += est.Estimate(m.Content) + 4 // ~4 tokens overhead per message
	}
	// Compute effective context window after reserving space for response
	effectiveContextWindow := maxContextTokens - cfg.LLM.MaxResponseTokens
	if effectiveContextWindow <= 0 {
		effectiveContextWindow = maxContextTokens
	}
	// Compute budget as in compress.Compress.
	var budgetFloat = float64(effectiveContextWindow) * 0.85
	budget := int(budgetFloat)
	margin := 0.15
	if margin < 0 {
		margin = 0
	}
	if margin >= 1 {
		margin = 0.99
	}
	budget = int(float64(budget) * (1 - margin))
	if total > budget {
		t.Fatalf("Total tokens %d exceed budget %d (effective context window %d)", total, budget, effectiveContextWindow)
	}
	// Additionally, verify that the total tokens are strictly less than the max context tokens
	// (leaving room for the response).
	if total >= maxContextTokens {
		t.Fatalf("Total tokens %d should be strictly less than max context tokens %d to leave room for response", total, maxContextTokens)
	}
	// For a long history, we expect the number of messages to be bounded.
	// We'll just check that it's less than, say, 50 messages (should be much less for 200 history messages with small context).
	if len(msgs) > 50 {
		t.Errorf("Number of messages %d seems too large for context window %d", len(msgs), maxContextTokens)
	}
}

// TestBuildMessagesRoleBasedToolAdvertisement tests that tools are properly
// advertised in the system prompt based on role permissions, regardless of
// policy filtering for specific users. This addresses the over-deflection bug
// where tools allowed by role were not mentioned in prompts due to policy
// filtering.
func TestBuildMessagesRoleBasedToolAdvertisement(t *testing.T) {
	tests := []struct {
		name               string
		userRole           string
		userAgeGroup       string
		webSearchEnabled   bool
		expectedInPrompt   bool // whether web_search should be in the prompt
		description        string
	}{
		{
			name:               "Child role with web_search enabled",
			userRole:           "child",
			userAgeGroup:       "age_13_17",
			webSearchEnabled:   true,
			expectedInPrompt:   true,
			description:       "Child role should see web_search in prompt when enabled",
		},
		{
			name:               "Child role with web_search disabled",
			userRole:           "child",
			userAgeGroup:       "age_13_17",
			webSearchEnabled:   false,
			expectedInPrompt:   false,
			description:       "Child role should not see web_search in prompt when disabled",
		},
		{
			name:               "Parent role with web_search enabled",
			userRole:           "parent",
			userAgeGroup:       "", // parents don't have age restrictions
			webSearchEnabled:   true,
			expectedInPrompt:   true,
			description:       "Parent role should see web_search in prompt when enabled",
		},
		{
			name:               "Parent role with web_search disabled",
			userRole:           "parent",
			userAgeGroup:       "",
			webSearchEnabled:   false,
			expectedInPrompt:   false,
			description:       "Parent role should not see web_search in prompt when disabled",
		},
		{
			name:               "Young child (under_8) with web_search",
			userRole:           "child",
			userAgeGroup:       "under_8",
			webSearchEnabled:   true,
			expectedInPrompt:   true, // Still advertised in prompt even if policy blocks
			description:       "Young child should see web_search advertised in prompt (role-based hint)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up a mock HTTP server to capture LLM calls
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Echo back a simple response
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"choices":[{"message":{"content":"test response"}}]}`))
			}))
			defer ts.Close()

			// Create a temporary database
			db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			// Create policy evaluator
			ev, err := policy.NewEvaluator("", "", "")
			if err != nil {
				t.Fatal(err)
			}

			// Configure the test scenario
			cfg := &config.Config{
				LLM: config.LLMConfig{
					BaseURL:           ts.URL,
					Model:             "test",
					Temperature:       0.7,
					MaxResponseTokens: 100,
				},
				Users: []config.UserConfig{
					{
						Name:        "testuser",
						DisplayName: "Test User",
						Role:        tt.userRole,
						AgeGroup:    tt.userAgeGroup,
					},
				},
				Tools: config.ToolsConfig{
					WebSearch: config.WebSearchConfig{
						Enabled: tt.webSearchEnabled,
					},
				},
			}

			// Build the builtin tools list based on config (similar to main.go)
			var builtinTools []agentcore.Tool
			var registered []string
			
			// Start with spawn_agent tool
			builtinTools = append(builtinTools, subagent.SpawnAgentTool())
			registered = append(registered, "spawn_agent")
			
			// Add web_fetch if enabled
			if cfg.Tools.WebFetch.Enabled {
				builtinTools = append(builtinTools, webfetch.Tool(cfg.Tools.WebFetch.AllowedRoles))
				registered = append(registered, "web_fetch")
			}
			
			// Add family state tools (always available)
			builtinTools = append(builtinTools, familystate.GetTool(), familystate.ProposeTool())
			registered = append(registered, "get_family_state", "propose_family_fact")
			
			// Add todo tool (always available)
			builtinTools = append(builtinTools, todo.Tool(nil))
			registered = append(registered, "todo")
			
			// Add user memory tools (always available)
			builtinTools = append(builtinTools,
				usermemory.RememberDefinition(),
				usermemory.RecallDefinition(),
				usermemory.ForgetDefinition(),
			)
			registered = append(registered, "remember_user_memory", "recall_user_memory", "forget_user_memory")
			
			// Add web_search if enabled
			if cfg.Tools.WebSearch.Enabled {
				builtinTools = append(builtinTools, websearch.Tool(cfg.Tools.WebSearch.AllowedRoles))
				registered = append(registered, "web_search")
			}
			
			// Add file tools (always available)
			builtinTools = append(builtinTools,
				filetool.FileReadTool(),
				filetool.FileWriteTool(),
				filetool.FileStatTool(),
				filetool.FileListTool(),
			)
			registered = append(registered, "file_read", "file_write", "file_stat", "file_list")
			
			// Create agent with the proper builtin tools
			a := NewAgent(&cfg.Users[0], cfg, nil, ev, nil, db, AgentDeps{
				BuiltinTools: builtinTools,
				// We don't need most dependencies for this test
			})

			// Build messages
			msgs := a.buildMessages(context.Background(), nil, "test message")
			if len(msgs) == 0 {
				t.Fatal("No messages returned")
			}

			// Check system prompt (first message)
			if msgs[0].Role != "system" {
				t.Fatalf("First message should be system prompt, got %s", msgs[0].Role)
			}

			systemPrompt := msgs[0].Content
			hasWebSearch := strings.Contains(systemPrompt, "web_search")

			if hasWebSearch != tt.expectedInPrompt {
				t.Fatalf("Expected web_search in prompt: %v, but got: %v. Prompt: %s",
					tt.expectedInPrompt, hasWebSearch, systemPrompt)
			}

			if tt.expectedInPrompt {
				t.Logf("✓ web_search correctly advertised in prompt for %s (%s)", tt.userRole, tt.userAgeGroup)
			} else {
				t.Logf("✓ web_search correctly NOT advertised in prompt for %s (%s)", tt.userRole, tt.userAgeGroup)
			}
		})
	}
}
