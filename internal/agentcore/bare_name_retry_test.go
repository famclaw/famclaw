package agentcore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestBareBuiltinFailedRetryIsDeduplicated(t *testing.T) {
	args := map[string]any{"url": "https://example.invalid"}
	chatter := &bareNameMockChatter{responses: []llm.Message{
		{ToolCalls: []llm.ToolCall{{ID: "retry", Function: llm.ToolCallFunction{Name: "web_fetch", Arguments: args}}}},
		{Content: "done"},
	}}
	calls := 0
	failure := errors.New("private failure")
	stage := NewStageToolLoop(ToolLoopDeps{
		MaxIterations: 2,
		ClientFactory: func(*Turn) llm.Chatter { return chatter },
		BuiltinHandler: func(_ context.Context, name string, _ map[string]any) (string, error) {
			calls++
			if name != "builtin__web_fetch" {
				t.Errorf("handler name = %q", name)
			}
			return "", failure
		},
	})
	turn := &Turn{User: &config.UserConfig{Name: "tester"}, Tools: []Tool{{Name: "builtin__web_fetch"}}}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "first", Function: llm.ToolCallFunction{Name: "web_fetch", Arguments: args}}})
	turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "fetch"}})
	if err := stage(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("handler calls = %d, want 1", calls)
	}
	if len(turn.ToolCalls) != 2 || !errors.Is(turn.ToolCalls[0].Error, failure) || turn.ToolCalls[0].Disposition != DispositionAttempted || !errors.Is(turn.ToolCalls[1].Error, ErrToolAlreadyFailed) || turn.ToolCalls[1].Disposition != DispositionSkipped {
		t.Fatalf("recorded calls = %+v", turn.ToolCalls)
	}
	var reply string
	for _, msg := range chatter.messages {
		if msg.Role == "tool" && msg.ToolCallID == "retry" {
			reply = msg.Content
		}
	}
	if !strings.Contains(reply, "already failed") {
		t.Errorf("retry reply = %q", reply)
	}
}

func TestBareDeniedRetryIsDeduplicatedAcrossIterations(t *testing.T) {
	args := map[string]any{"url": "https://example.invalid"}
	chatter := &bareNameMockChatter{responses: []llm.Message{
		{ToolCalls: []llm.ToolCall{{ID: "retry", Function: llm.ToolCallFunction{Name: "web_fetch", Arguments: args}}}},
		{Content: "done"},
	}}
	calls := 0
	stage := NewStageToolLoop(ToolLoopDeps{
		MaxIterations: 2,
		ClientFactory: func(*Turn) llm.Chatter { return chatter },
		BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
			calls++
			return "", nil
		},
	})
	turn := &Turn{User: &config.UserConfig{Name: "tester"}, Tools: []Tool{{Name: "builtin__unrelated"}}}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "first", Function: llm.ToolCallFunction{Name: "web_fetch", Arguments: args}}})
	turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "fetch"}})
	if err := stage(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("handler calls = %d, want 0", calls)
	}
	if len(turn.ToolCalls) != 2 {
		t.Fatalf("recorded calls = %+v", turn.ToolCalls)
	}
	first, retry := turn.ToolCalls[0], turn.ToolCalls[1]
	if first.ToolName != "web_fetch" || first.Disposition != DispositionDenied || first.Error == nil {
		t.Errorf("first denial = %+v", first)
	}
	if retry.ToolName != "web_fetch" || retry.Disposition != DispositionSkipped || !errors.Is(retry.Error, ErrToolAlreadyFailed) {
		t.Errorf("duplicate = %+v", retry)
	}
	var reply string
	for _, msg := range chatter.messages {
		if msg.Role == "tool" && msg.ToolCallID == "retry" {
			reply = msg.Content
		}
	}
	if !strings.Contains(reply, "not available for this turn") {
		t.Errorf("retry reply = %q", reply)
	}
}
