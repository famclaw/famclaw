package agentcore

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestOutcomeBuiltinSinks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure bool
		rebuild bool
	}{
		{"failure_legacy", true, false}, {"failure_rebuilt", true, true}, {"success", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// "fake-*" fixture: synthetic test marker, not a live secret. The
			// check-private-details guard (CREDENTIAL shape) only exempts the
			// documented placeholder conventions (fake-*/test-*/changeme/...),
			// so keep this value inside that allowlist.
			const secret = "fake-private-marker"
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			chatter := &leakChatter{}
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: "builtin__operation"}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "test-id", Function: llm.ToolCallFunction{Name: "builtin__operation", Arguments: map[string]any{"token": secret}}}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			deps := ToolLoopDeps{RebuildHistory: tc.rebuild, MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter }, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
				if tc.failure {
					return "", errors.New(secret)
				}
				return "safe-success", nil
			}}
			if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if len(turn.ToolCalls) != 1 {
				t.Fatal("missing result")
			}
			if tc.failure {
				if turn.ToolCalls[0].Error == nil {
					t.Error("failure recorded as success")
				}
				if strings.Contains(logs.String(), secret) {
					t.Error("private cause leaked to logs")
				}
				for _, call := range chatter.calls {
					for _, msg := range call.msgs {
						if strings.Contains(msg.Content, secret) {
							t.Error("private cause leaked to model")
						}
					}
				}
			} else if turn.ToolCalls[0].Error != nil || turn.ToolCalls[0].Output != "safe-success" {
				t.Error("success not preserved")
			}
		})
	}
}
