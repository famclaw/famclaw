package agentcore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestOfflineSearchExcerptsPolicyAndBoundaries(t *testing.T) {
	search := llm.ToolCall{ID: "s", Function: llm.ToolCallFunction{Name: "builtin__web_search", Arguments: map[string]any{"query": "fixture"}}}
	write := llm.ToolCall{ID: "w", Function: llm.ToolCallFunction{Name: "builtin__file_write", Arguments: map[string]any{"content": "fixture"}}}
	for _, tc := range []struct {
		name                                                             string
		record, fail, pendingWrite, priorWrite, priorApproval, finalText bool
		wantExcerpt                                                      bool
	}{
		{name: "structured success", record: true, wantExcerpt: true},
		{name: "unsupported plain result", wantExcerpt: false},
		{name: "failed search cannot supply excerpts", record: true, fail: true, wantExcerpt: false},
		{name: "pending write remains unrun", record: true, pendingWrite: true, wantExcerpt: true},
		{name: "prior write remains authoritative", record: true, priorWrite: true, wantExcerpt: true},
		{name: "pending approval remains authoritative", record: true, priorApproval: true, wantExcerpt: true},
		{name: "existing model answer stays intact", record: true, finalText: true, wantExcerpt: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls, modelCalls int
			turn := &Turn{User: &config.UserConfig{Name: "test", Role: "parent"}, Tools: []Tool{{Name: "builtin__web_search", ReadOnly: true}, {Name: "builtin__file_write"}}}
			if tc.priorWrite {
				turn.ToolCalls = []ToolResult{{ToolName: "builtin__file_write", Disposition: DispositionAttempted, Outcome: OutcomeFailure, Error: errors.New("write uncertain")}}
			}
			if tc.priorApproval {
				turn.ToolCalls = []ToolResult{{ToolName: "builtin__file_write", Disposition: DispositionPending}}
			}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{search})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "fixture"}})
			answer := llm.Message{}
			if tc.pendingWrite {
				answer.ToolCalls = []llm.ToolCall{write}
			}
			if tc.finalText {
				answer.Content = "Model answer."
			}
			chatter := &mockChatter{responses: []llm.Message{answer}, callCount: &modelCalls}
			stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter }, BuiltinHandler: func(ctx context.Context, name string, args map[string]any) (string, error) {
				calls++
				if name != "builtin__web_search" {
					t.Fatalf("unexpected tool execution %s", name)
				}
				if tc.record {
					RecordSearchExcerpts(ctx, []SearchExcerpt{{Title: "fixture title", URL: "https://example.invalid/a", Snippet: "fixture snippet"}})
				}
				if tc.fail {
					return "", errors.New("failed")
				}
				return "ordinary search output", nil
			}})
			if err := stage(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || modelCalls != 1 {
				t.Fatalf("extra call: handler=%d model=%d", calls, modelCalls)
			}
			if strings.Contains(turn.Output, "fixture snippet") != tc.wantExcerpt {
				t.Fatalf("unexpected output %q", turn.Output)
			}
			if tc.pendingWrite && !strings.Contains(turn.Output, "remaining requested calls were not run") {
				t.Fatalf("missing pending disclosure %q", turn.Output)
			}
			if tc.priorWrite && !strings.Contains(turn.Output, "changes may have occurred") {
				t.Fatalf("missing write warning %q", turn.Output)
			}
			if tc.priorApproval && !strings.Contains(turn.Output, "pending approval and was not run") {
				t.Fatalf("approval disclosure missing: %q", turn.Output)
			}
			if tc.finalText && turn.Output != "Model answer." {
				t.Fatalf("answer altered: %q", turn.Output)
			}
		})
	}
}

func TestOfflineSearchExcerptsHostileInputInertAndBounded(t *testing.T) {
	sink := &searchExcerptSink{}
	ctx := context.WithValue(context.Background(), searchExcerptKey{}, sink)
	attack := "<img src=x onerror=bad()> ![image](https://bad.example) `$(evil)`"
	RecordSearchExcerpts(ctx, []SearchExcerpt{{Title: attack, URL: "https://example.invalid/a", Snippet: attack}})
	if len(sink.values) != 1 {
		t.Fatal("escaped source should be retained")
	}
	rendered := renderSearchExcerpts(sink.values)
	if strings.Contains(rendered, "<img") || strings.Contains(rendered, "![image](https://bad.example)") {
		t.Fatalf("active markup leaked: %q", rendered)
	}
	sink.values = nil
	RecordSearchExcerpts(ctx, []SearchExcerpt{{Title: "fixture", URL: "https://example.invalid/a", Snippet: strings.Repeat("A", 30000)}, {Title: "next", URL: "https://example.invalid/b"}, {Title: "third", URL: "https://example.invalid/c"}, {Title: "excluded", URL: "https://example.invalid/d"}})
	result := renderSearchExcerpts(sink.values)
	if len(sink.values) != 3 || len([]rune(result)) > 1200 {
		t.Fatalf("not bounded: %d / %d", len(sink.values), len([]rune(result)))
	}
	if !strings.Contains(result, "Search snippets (full pages not checked)") {
		t.Fatal("missing label")
	}
}

func TestOfflineSearchExcerptsPreservesNumericFacts(t *testing.T) {
	got := excerptText("Weight -3.5 kg, rate 12.5%", 240)
	if got != `Weight \-3\.5 kg, rate 12\.5%` {
		t.Fatalf("numeric facts changed: %q", got)
	}
}

func TestSearchExcerptRejectsUnsafeLinks(t *testing.T) {
	for _, raw := range []string{"javascript:alert(1)", "https://", "https://user:pass@example.invalid/", "https://example.invalid/a)![x](https://evil.invalid/x", "https://example.invalid/<script>", "https://example.invalid/\nother"} {
		if got := excerptURL(raw); got != "" {
			t.Errorf("unsafe URL accepted %q", raw)
		}
	}
	for _, raw := range []string{"https://example.invalid/a?x=1&y=2", "http://example.invalid/article"} {
		if got := excerptURL(raw); got != raw {
			t.Errorf("valid URL rejected %q -> %q", raw, got)
		}
	}
}
