package agentcore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestOutcomePartialEffects(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{{"partial", true}, {"success", false}} {
		t.Run(tc.name, func(t *testing.T) {
			effects := 0
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: "builtin__write"}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "call1", Function: llm.ToolCallFunction{Name: "builtin__write", Arguments: map[string]any{}}}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "write"}})
			count := 0
			deps := ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter {
				return &mockChatter{responses: []llm.Message{{Content: "Done."}}, callCount: &count}
			}, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
				effects++
				if tc.fail {
					return "", errors.New("partial failure")
				}
				return "ok", nil
			}}
			if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if effects != 1 {
				t.Fatalf("effects=%d; want 1", effects)
			}
			if tc.fail && strings.Contains(turn.Output, "No action was completed") {
				t.Error("partial effect incorrectly described as no action completed")
			}
			if !tc.fail && turn.Output != "Done." {
				t.Errorf("success output = %q", turn.Output)
			}
		})
	}
}
