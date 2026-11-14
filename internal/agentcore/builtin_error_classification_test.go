package agentcore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestBuiltinFailureClassificationInToolLoop(t *testing.T) {
	for _, tc := range []struct {
		name, class string
		cause       error
		readOnly    bool
	}{
		{"timeout", "Error class: timeout.", context.DeadlineExceeded, false},
		{"cancelled", "Error class: cancelled.", context.Canceled, true},
		{"not_found", "Error class: not found.", fs.ErrNotExist, false},
		{"permission", "Error class: permission denied.", fs.ErrPermission, true},
		{"unknown", "", errors.New("other"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const secret = "fake-private-host-or-path"
			cause := fmt.Errorf("%s: %w", secret, tc.cause)
			chatter := &bareNameMockChatter{responses: []llm.Message{{Content: "done"}}}
			calls := 0
			turn := &Turn{User: &config.UserConfig{Name: "tester"}, Tools: []Tool{{Name: "builtin__operation", ReadOnly: tc.readOnly}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{
				{ID: "one", Function: llm.ToolCallFunction{Name: "builtin__operation", Arguments: map[string]any{"x": "same"}}},
				{ID: "two", Function: llm.ToolCallFunction{Name: "builtin__operation", Arguments: map[string]any{"x": "same"}}},
			})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter },
				BuiltinHandler: func(context.Context, string, map[string]any) (string, error) { calls++; return secret, cause }})
			if err := stage(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || len(turn.ToolCalls) != 2 || !errors.Is(turn.ToolCalls[1].Error, ErrToolAlreadyFailed) {
				t.Fatalf("retry/dispatch: calls=%d results=%+v", calls, turn.ToolCalls)
			}
			first := turn.ToolCalls[0]
			if first.Disposition != DispositionAttempted || first.Outcome != OutcomeFailure || !errors.Is(first.Error, tc.cause) {
				t.Fatalf("outcome = %+v", first)
			}
			if strings.Contains(first.Output, secret) || (tc.class != "" && !strings.Contains(first.Output, tc.class)) {
				t.Errorf("unsafe or missing class: %q", first.Output)
			}
			if tc.class == "" && first.Output != ToolErrorDisplayChangesMayHaveOccurred {
				t.Errorf("unknown class = %q", first.Output)
			}
			if !tc.readOnly && !strings.Contains(first.Output, "some changes may still have occurred") {
				t.Errorf("write uncertainty lost: %q", first.Output)
			}
			if tc.readOnly && !strings.Contains(first.Output, "effects are not verified") {
				t.Errorf("read-only caveat lost: %q", first.Output)
			}
			var sawClass bool
			for _, msg := range chatter.messages {
				if strings.Contains(msg.Content, secret) {
					t.Fatal("private detail leaked to model")
				}
				if tc.class != "" && strings.Contains(msg.Content, tc.class) {
					sawClass = true
				}
			}
			if tc.class != "" && !sawClass {
				t.Fatal("classified tool result did not reach model")
			}
		})
	}
}

func TestBuiltinSearchSafeMarkerPrecedesErrorClass(t *testing.T) {
	chatter := &bareNameMockChatter{responses: []llm.Message{{Content: "done"}}}
	turn := &Turn{User: &config.UserConfig{Name: "tester"}, Tools: []Tool{{Name: "builtin__web_search", ReadOnly: true}}}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "one", Function: llm.ToolCallFunction{Name: "builtin__web_search"}}})
	turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
	stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter }, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
		return WebSearchMarkerRateLimited, context.DeadlineExceeded
	}})
	if err := stage(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Output != WebSearchMarkerRateLimited {
		t.Fatalf("search marker changed: %+v", turn.ToolCalls)
	}
}
