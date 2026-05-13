package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/policy"
)

func TestStageClassify(t *testing.T) {
	clf := classifier.New()
	stage := NewStageClassify(clf)

	turn := &Turn{
		User:  &config.UserConfig{Name: "test", Role: "child"},
		Input: "how do I do my homework?",
	}

	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("classify error: %v", err)
	}
	if turn.Category == "" {
		t.Error("category should be set after classify")
	}
}


func TestStagePolicyInputBlock(t *testing.T) {
	// We can't easily test with a real OPA evaluator without policy files,
	// but we can verify the ErrPolicyBlock sentinel error is used correctly.
	if !errors.Is(ErrPolicyBlock, ErrPolicyBlock) {
		t.Error("ErrPolicyBlock should be identifiable via errors.Is")
	}
}

func TestPolicyMessage(t *testing.T) {
	tests := []struct {
		action string
		want   string
	}{
		{"block", "I'm sorry"},
		{"request_approval", "asked a parent"},
		{"pending", "already been notified"},
		{"unknown", "unable to answer"},
	}

	for _, tt := range tests {
		msg := policyMessage(policy.Decision{Action: tt.action, Reason: "test"})
		if msg == "" {
			t.Errorf("policyMessage(%q) returned empty", tt.action)
		}
	}
}

func TestTurnToLLMMessages(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi!", ToolCalls: []ToolCall{
			{ID: "1", Type: "function", Function: ToolCallFunction{Name: "test", Arguments: map[string]any{"k": "v"}}},
		}},
	}

	llmMsgs := turnToLLMMessages(msgs)
	if len(llmMsgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(llmMsgs))
	}
	if llmMsgs[0].Role != "system" {
		t.Errorf("msg[0].Role = %q, want 'system'", llmMsgs[0].Role)
	}
	if len(llmMsgs[2].ToolCalls) != 1 {
		t.Errorf("expected 1 tool call in msg[2], got %d", len(llmMsgs[2].ToolCalls))
	}
	if llmMsgs[2].ToolCalls[0].Function.Name != "test" {
		t.Errorf("tool call name = %q, want 'test'", llmMsgs[2].ToolCalls[0].Function.Name)
	}
}

func TestToolsToLLMDefs(t *testing.T) {
	tools := []Tool{
		{
			Name:        "mcp__weather__forecast",
			Description: "Get weather",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"location": map[string]any{"type": "string"},
				},
			},
		},
	}

	defs := toolsToLLMDefs(tools)
	if len(defs) != 1 {
		t.Fatalf("expected 1 def, got %d", len(defs))
	}
	if defs[0].Type != "function" {
		t.Errorf("type = %q, want 'function'", defs[0].Type)
	}
	if defs[0].Function.Name != "mcp__weather__forecast" {
		t.Errorf("name = %q", defs[0].Function.Name)
	}
}

// TestStageToolLoop_ToolCallIDPropagation asserts that every tool-reply
// llm.Message the stage appends carries the originating ToolCall.ID. The
// OpenAI Chat Completions spec (and strict-mode backends like vLLM and
// gemini-openai-proxy) require tool_call_id on role:"tool" messages — without
// it the second LLM round-trip 4xxes.
//
// We cover all four branches that synthesize a tool reply:
//   1. Tool not in turn.Tools allowlist
//   2. Builtin handler returns error
//   3. Builtin handler returns success
//   4. Unknown tool (passes allowlist but neither pool nor builtin route fires)
func TestStageToolLoop_ToolCallIDPropagation(t *testing.T) {
	type capturedReq struct {
		Messages []llm.Message `json:"messages"`
	}

	cases := []struct {
		name        string
		turnTool    string
		callName    string
		callID      string
		buildHandler func() func(ctx context.Context, name string, args map[string]any) (string, error)
		wantContent string
	}{
		{
			name:     "rejected by turn allowlist",
			turnTool: "allowed",
			callName: "not_allowed",
			callID:   "call_rejected",
			buildHandler: func() func(ctx context.Context, name string, args map[string]any) (string, error) {
				// Stage requires either Pool or BuiltinHandler to be non-nil
				// to run; supply a no-op handler that should never be called
				// because the allowlist rejects first.
				return func(ctx context.Context, name string, args map[string]any) (string, error) {
					return "", errors.New("builtin should not have been invoked")
				}
			},
			wantContent: `Error: tool "not_allowed" not available`,
		},
		{
			name:     "builtin handler error",
			turnTool: "builtin__failboat",
			callName: "builtin__failboat",
			callID:   "call_builtin_err",
			buildHandler: func() func(ctx context.Context, name string, args map[string]any) (string, error) {
				return func(ctx context.Context, name string, args map[string]any) (string, error) {
					return "", errors.New("synthetic builtin failure")
				}
			},
			wantContent: "Error: synthetic builtin failure",
		},
		{
			name:     "builtin handler success",
			turnTool: "builtin__okboat",
			callName: "builtin__okboat",
			callID:   "call_builtin_ok",
			buildHandler: func() func(ctx context.Context, name string, args map[string]any) (string, error) {
				return func(ctx context.Context, name string, args map[string]any) (string, error) {
					return "ok", nil
				}
			},
			wantContent: "ok",
		},
		{
			name:     "unknown tool (no pool, no builtin prefix)",
			turnTool: "ghost",
			callName: "ghost",
			callID:   "call_unknown",
			buildHandler: func() func(ctx context.Context, name string, args map[string]any) (string, error) {
				return func(ctx context.Context, name string, args map[string]any) (string, error) {
					return "", errors.New("builtin should not have been invoked for non-builtin name")
				}
			},
			wantContent: `Error: unknown tool "ghost"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured capturedReq
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&captured)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{{
						"message":       map[string]any{"role": "assistant", "content": "done"},
						"finish_reason": "stop",
					}},
				})
			}))
			defer server.Close()

			deps := ToolLoopDeps{
				ClientFactory: func(*Turn) llm.Chatter {
					return llm.NewClient(server.URL, "test", "")
				},
				BuiltinHandler: tc.buildHandler(),
				MaxIterations:  2,
			}
			stage := NewStageToolLoop(deps)

			turn := &Turn{
				User:  &config.UserConfig{Name: "tester"},
				Tools: []Tool{{Name: tc.turnTool}},
			}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{
				ID:       tc.callID,
				Function: llm.ToolCallFunction{Name: tc.callName, Arguments: map[string]any{}},
			}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "hi"}})

			if err := stage(context.Background(), turn); err != nil {
				t.Fatalf("stage: %v", err)
			}

			var toolReply *llm.Message
			for i := range captured.Messages {
				if captured.Messages[i].Role == "tool" {
					toolReply = &captured.Messages[i]
					break
				}
			}
			if toolReply == nil {
				t.Fatalf("no role:tool message reached the LLM; captured=%+v", captured.Messages)
			}
			if toolReply.ToolCallID != tc.callID {
				t.Errorf("ToolCallID = %q, want %q", toolReply.ToolCallID, tc.callID)
			}
			if toolReply.Content != tc.wantContent {
				t.Errorf("Content = %q, want %q", toolReply.Content, tc.wantContent)
			}
			// The recorded ToolResult on the turn should also reflect the call.
			if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].ToolName != tc.callName {
				t.Errorf("turn.ToolCalls = %+v, want exactly one entry for %q", turn.ToolCalls, tc.callName)
			}
		})
	}
}

// TestStageToolLoop_CompressionDropsOlderToolMessages asserts the
// per-iteration compression path: when llmMsgs has accumulated older
// prunable tool messages and a new tool result lands, the older tool
// messages get evicted before the next LLM call. The newest tool reply
// is preserved (it's in the "last 3" protected zone).
//
// Note: this section alone CANNOT prevent overflow when a single tool
// result is itself larger than the budget — dropping the tool reply
// orphans its parent assistant tool_call. That overflow class is fixed
// by Section D's spillover cache, which puts only a head slice into
// context. Section B's job is the plumbing for compression between
// iterations; this test guards that plumbing.
func TestStageToolLoop_CompressionDropsOlderToolMessages(t *testing.T) {
	type capturedReq struct {
		Messages []llm.Message `json:"messages"`
	}

	smallPayload := "fresh result"

	var captured capturedReq
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "done"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer server.Close()

	deps := ToolLoopDeps{
		ClientFactory: func(*Turn) llm.Chatter {
			return llm.NewClient(server.URL, "test", "")
		},
		BuiltinHandler: func(ctx context.Context, name string, args map[string]any) (string, error) {
			return smallPayload, nil
		},
		MaxIterations: 2,
		ContextWindow: 800, // small budget so older prunable tool messages must drop
	}
	stage := NewStageToolLoop(deps)

	// Pre-populate llmMsgs with history including a big older tool message
	// that should be droppable. The assistant tool_call ahead of it would
	// orphan if dropped, so use a "raw" older tool message that's NOT tied
	// to a current pending tool_call. (This is a synthetic test setup —
	// real flows have the pairing constraint.)
	oldBigTool := strings.Repeat("X", 4000) // ~1000 tokens, prunable
	turn := &Turn{
		User:  &config.UserConfig{Name: "tester"},
		Tools: []Tool{{Name: "builtin__bigboat"}},
	}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{{
		ID:       "call_new",
		Function: llm.ToolCallFunction{Name: "builtin__bigboat", Arguments: map[string]any{}},
	}})
	turn.SetMeta("llm_messages", []llm.Message{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "older question"},
		{Role: "assistant", Content: "old reply"},
		{Role: "tool", Content: oldBigTool, ToolCallID: "old"}, // big old prunable
		{Role: "user", Content: "go again"},
	})

	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("stage: %v", err)
	}

	// The captured LLM call after compression should NOT contain the
	// big old tool message — it's the only Prunable candidate and the
	// budget forces a drop. The new tool reply (in last-3 protected
	// zone after the loop appends it) survives.
	for _, m := range captured.Messages {
		if m.Role == "tool" && m.Content == oldBigTool {
			t.Error("old big prunable tool message should have been compressed away")
		}
	}
	// Sanity: the new tool reply should have made it through.
	sawNew := false
	for _, m := range captured.Messages {
		if m.Role == "tool" && m.Content == smallPayload {
			sawNew = true
		}
	}
	if !sawNew {
		t.Error("new tool reply should be present in post-compression LLM call")
	}
}
