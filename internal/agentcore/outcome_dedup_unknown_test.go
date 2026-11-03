package agentcore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestOutcomeDedupUnknown(t *testing.T) {
	for _, tc := range []struct {
		name      string
		outcome   ExecutionOutcome
		priorErr  error
		wantCalls int
		marker    string
	}{
		{"unknown", OutcomeUnknown, errors.New("transport"), 0, "uncertain"},
		{"unknown without diagnostic", OutcomeUnknown, nil, 0, "uncertain"},
		{"failure", OutcomeFailure, errors.New("failure"), 0, "already failed"},
		{"success", OutcomeSuccess, nil, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const name = "builtin__operation"
			args := map[string]any{"key": "value"}
			chatter := &dedupChatter{responses: []llm.Message{{Role: "assistant", Content: "done"}}}
			calls := 0
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: name}}, ToolCalls: []ToolResult{{ToolName: name, Args: args, Error: tc.priorErr, Outcome: tc.outcome, Disposition: DispositionAttempted}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "retry", Function: llm.ToolCallFunction{Name: name, Arguments: args}}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 2, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) { calls++; return "ok", nil }, ClientFactory: func(*Turn) llm.Chatter { return chatter }})
			if err := stage(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if calls != tc.wantCalls {
				t.Errorf("calls = %d, want %d", calls, tc.wantCalls)
			}
			if tc.marker != "" {
				found := false
				for _, batch := range chatter.captured {
					for _, msg := range batch {
						if msg.Role == "tool" && msg.ToolCallID == "retry" && strings.Contains(strings.ToLower(msg.Content), tc.marker) {
							found = true
						}
					}
				}
				if !found {
					t.Errorf("retry response missing %q", tc.marker)
				}
			}
		})
	}
}
