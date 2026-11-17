package agentcore

import (
	"context"
	"errors"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"reflect"
	"strings"
	"testing"
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

// An unknown tool is never executed; repeated identical calls must tell the
// model that the earlier call was skipped rather than imply execution failed.
func TestOutcomeDedupSkippedUnknownTool(t *testing.T) {
	const name = "mcp__missing"
	args := map[string]any{"key": "value"}
	chatter := &dedupChatter{responses: []llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "retry", Function: llm.ToolCallFunction{Name: name, Arguments: map[string]any{"key": "value"}}}}},
		{Role: "assistant", Content: "done"},
	}}
	calls := 0
	turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: name}}}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "first", Function: llm.ToolCallFunction{Name: name, Arguments: args}}})
	turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
	stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 3, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) { calls++; return "ok", nil }, ClientFactory: func(*Turn) llm.Chatter { return chatter }})
	if err := stage(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("unexpected tool executions: %d", calls)
	}
	if len(turn.ToolCalls) != 2 {
		t.Fatalf("results: %+v", turn.ToolCalls)
	}
	if turn.ToolCalls[0].Disposition != DispositionSkipped || turn.ToolCalls[0].Error == nil {
		t.Fatalf("original result: %+v", turn.ToolCalls[0])
	}
	if turn.ToolCalls[1].Disposition != DispositionSkipped || !errors.Is(turn.ToolCalls[1].Error, ErrToolAlreadyFailed) {
		t.Fatalf("retry result: %+v", turn.ToolCalls[1])
	}
	for _, batch := range chatter.captured {
		for _, msg := range batch {
			if msg.Role == "tool" && msg.ToolCallID == "retry" {
				text := strings.ToLower(msg.Content)
				if !strings.Contains(text, "skipped") || strings.Contains(text, "failed") || strings.Contains(text, "executed") {
					t.Fatalf("misleading retry feedback: %q", msg.Content)
				}
				return
			}
		}
	}
	t.Fatal("retry feedback did not reach the model")
}

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
	if strings.Count(history, "skip=tool unavailable; not run") != 2 || strings.Contains(history, "err=") {
		t.Fatalf("rebuilt history: %q", history)
	}
}

func TestReadOnlyIndexCacheMetadataChanges(t *testing.T) {
	tools := []Tool{{Name: "mcp__a__lookup", ReadOnly: true}, {Name: "mcp__b__lookup", ReadOnly: false}, {Name: "builtin__echo", ReadOnly: true}}
	var c readOnlyIndexCache
	for _, step := range []struct {
		name   string
		change func()
	}{
		{"initial collision", func() {}},
		{"in-place flag", func() { tools[0].ReadOnly = false }},
		{"rename", func() { tools[1].Name = "mcp__b__other" }},
		{"append", func() { tools = append(tools, Tool{Name: "lookup", ReadOnly: true}) }},
		{"remove", func() { tools = tools[:2] }},
		{"duplicate exact", func() { tools = append(tools, Tool{Name: tools[0].Name, ReadOnly: true}) }},
	} {
		t.Run(step.name, func(t *testing.T) {
			step.change()
			got := c.get(tools)
			want := buildReadOnlyIndex(tools)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v want %v", got, want)
			}
		})
	}
}

func TestReadOnlyIndexCacheWarmAllocations(t *testing.T) {
	tools := []Tool{{Name: "mcp__one__lookup", ReadOnly: true}, {Name: "builtin__echo"}}
	var c readOnlyIndexCache
	_ = c.get(tools)
	if n := testing.AllocsPerRun(100, func() { _ = turnReadOnlyLookup(c.get(tools), "lookup") }); n != 0 {
		t.Fatalf("warm metadata lookup allocations = %v, want 0", n)
	}
}

func TestToolLoopFinalWarningUsesCurrentToolMetadata(t *testing.T) {
	turn := newRebuildTurn("go")
	chatter := newStubChatter(0)
	stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter }, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
		turn.Tools[0].ReadOnly = true
		return "", errors.New("test failure")
	}})
	if err := stage(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(turn.Output, "marked read-only") {
		t.Fatalf("warning did not use changed metadata: %q", turn.Output)
	}
}

func BenchmarkReadOnlyIndexCacheWarmLookup(b *testing.B) {
	tools := []Tool{{Name: "mcp__one__lookup", ReadOnly: true}, {Name: "builtin__echo"}}
	var c readOnlyIndexCache
	_ = c.get(tools)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = turnReadOnlyLookup(c.get(tools), "lookup")
	}
}
