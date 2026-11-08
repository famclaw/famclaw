package agentcore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestRebuildResultLineSkippedState(t *testing.T) {
	const secret = "fake-private-detail"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"intentional skip", nil, "skip=skipped; not run"},
		{"skipped with error", errors.New(secret), "skip=skipped; not run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := rebuildResultLine(ToolResult{ToolName: "builtin__browser_extract", Args: map[string]any{"secret": secret}, Error: tc.err, Disposition: DispositionSkipped})
			if !strings.Contains(line, tc.want) || strings.Contains(line, secret) || strings.Contains(line, "ok bytes=") {
				t.Errorf("history line = %q, want %q and no private detail or success", line, tc.want)
			}
		})
	}
}

// Rebuilt model history must identify both unavailable and deduplicated calls
// as skipped while retaining their diagnostic errors in the turn state.
func TestRebuildHistorySkippedUnknownTool(t *testing.T) {
	const name = "mcp__missing"
	args := map[string]any{"key": "value"}
	chatter := &dedupChatter{responses: []llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "retry", Function: llm.ToolCallFunction{Name: name, Arguments: args}}}},
		{Role: "assistant", Content: "done"},
	}}
	calls := 0
	turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: name}}}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "first", Function: llm.ToolCallFunction{Name: name, Arguments: args}}})
	turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
	stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 3, RebuildHistory: true, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) { calls++; return "ok", nil }, ClientFactory: func(*Turn) llm.Chatter { return chatter }})
	if err := stage(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(turn.ToolCalls) != 2 {
		t.Fatalf("executions=%d, results=%+v", calls, turn.ToolCalls)
	}
	for _, result := range turn.ToolCalls {
		if result.Disposition != DispositionSkipped || result.Error == nil {
			t.Fatalf("result lost skipped state or diagnostic: %+v", result)
		}
	}
	if !errors.Is(turn.ToolCalls[1].Error, ErrToolAlreadyFailed) {
		t.Fatalf("retry diagnostic: %v", turn.ToolCalls[1].Error)
	}
	if len(chatter.captured) != 2 {
		t.Fatalf("model calls=%d", len(chatter.captured))
	}
	var history string
	for _, message := range chatter.captured[1] {
		history += message.Content
	}
	if strings.Count(history, "skip=skipped; not run") != 2 || strings.Contains(history, "err=skipped; not run") {
		t.Fatalf("rebuilt history: %q", history)
	}
}
