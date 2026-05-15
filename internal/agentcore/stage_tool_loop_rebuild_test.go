package agentcore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

// stubChatter is a minimal llm.Chatter used exclusively in rebuild-history
// tests. It does not require a real LLM endpoint.
//
// toolCallRounds controls how many ChatWithTools calls return a tool call
// (builtin__echo). Subsequent calls return a plain "done" message.
// All calls are recorded in captured for inspection.
type stubChatter struct {
	mu             sync.Mutex
	callCount      int
	toolCallRounds int
	captured       [][]llm.Message
}

func newStubChatter(toolCallRounds int) *stubChatter {
	return &stubChatter{toolCallRounds: toolCallRounds}
}

func (s *stubChatter) Chat(_ context.Context, _ []llm.Message, _ float64, _ int, _ func(string)) (string, error) {
	return "done", nil
}

func (s *stubChatter) ChatMessage(_ context.Context, _ []llm.Message, _ float64, _ int) (*llm.Message, error) {
	return &llm.Message{Role: "assistant", Content: "done"}, nil
}

func (s *stubChatter) ChatSync(_ context.Context, _ []llm.Message, _ float64, _ int) (string, error) {
	return "done", nil
}

func (s *stubChatter) ChatWithTools(_ context.Context, msgs []llm.Message, _ float64, _ int, _ []llm.ToolDef) (*llm.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.callCount++
	call := s.callCount

	captured := make([]llm.Message, len(msgs))
	copy(captured, msgs)
	s.captured = append(s.captured, captured)

	if call <= s.toolCallRounds {
		return &llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:       fmt.Sprintf("call_%d", call),
				Function: llm.ToolCallFunction{Name: "builtin__echo", Arguments: map[string]any{}},
			}},
		}, nil
	}
	return &llm.Message{Role: "assistant", Content: "done"}, nil
}

// newRebuildTurn builds a Turn with a single pending builtin__echo tool call
// and an llm_messages slice containing one system and one user message.
func newRebuildTurn(userRequest string) *Turn {
	t := &Turn{
		User:  &config.UserConfig{Name: "tester"},
		Tools: []Tool{{Name: "builtin__echo"}},
	}
	t.SetMeta("pending_tool_calls", []llm.ToolCall{{
		ID:       "call_0",
		Function: llm.ToolCallFunction{Name: "builtin__echo", Arguments: map[string]any{}},
	}})
	t.SetMeta("llm_messages", []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: userRequest},
	})
	return t
}

// echoBuiltin is a minimal BuiltinHandler that accepts builtin__echo and
// returns "ok".
func echoBuiltin(_ context.Context, _ string, _ map[string]any) (string, error) {
	return "ok", nil
}

// TestToolLoop_RebuildHistory_DisabledByDefault verifies that when
// RebuildHistory is not set (false), the legacy append path runs: the
// llmMsgs slice sent to the LLM GROWS between iterations because each
// tool result and assistant message is appended.
func TestToolLoop_RebuildHistory_DisabledByDefault(t *testing.T) {
	stub := newStubChatter(1) // call 1 → tool call; call 2 → done
	deps := ToolLoopDeps{
		ClientFactory:  func(*Turn) llm.Chatter { return stub },
		BuiltinHandler: echoBuiltin,
		MaxIterations:  10,
		// RebuildHistory intentionally omitted — must default to false
	}
	stage := NewStageToolLoop(deps)
	turn := newRebuildTurn("do something")

	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(stub.captured) != 2 {
		t.Fatalf("expected exactly 2 LLM calls, got %d", len(stub.captured))
	}

	iter1Len := len(stub.captured[0])
	iter2Len := len(stub.captured[1])
	if iter2Len <= iter1Len {
		t.Errorf("legacy append path: messages should grow; iter1=%d iter2=%d", iter1Len, iter2Len)
	}
}

// TestToolLoop_RebuildHistory_EnabledRebuildsEveryTurn verifies that when
// RebuildHistory is true the second LLM call receives exactly [system, user]
// (2 messages) and the user message contains the expected history markers.
func TestToolLoop_RebuildHistory_EnabledRebuildsEveryTurn(t *testing.T) {
	stub := newStubChatter(1)
	deps := ToolLoopDeps{
		ClientFactory:  func(*Turn) llm.Chatter { return stub },
		BuiltinHandler: echoBuiltin,
		MaxIterations:  10,
		RebuildHistory: true,
	}
	stage := NewStageToolLoop(deps)
	turn := newRebuildTurn("do something")

	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(stub.captured) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(stub.captured))
	}

	iter2 := stub.captured[1]
	if len(iter2) != 2 {
		t.Errorf("rebuild mode: iter2 should have exactly 2 messages (system+user), got %d", len(iter2))
	}
	if len(iter2) < 2 {
		t.FailNow()
	}

	userContent := iter2[1].Content
	for _, want := range []string{"<agent_history>", "<step_1>", "builtin__echo"} {
		if !strings.Contains(userContent, want) {
			t.Errorf("iter2 user message missing %q\ncontent: %s", want, userContent)
		}
	}
}

// TestToolLoop_RebuildHistory_PreservesUserRequest verifies that the original
// user request survives in the rebuilt user message across iterations.
func TestToolLoop_RebuildHistory_PreservesUserRequest(t *testing.T) {
	const request = "find flights TPA to MSY"
	stub := newStubChatter(1)
	deps := ToolLoopDeps{
		ClientFactory:  func(*Turn) llm.Chatter { return stub },
		BuiltinHandler: echoBuiltin,
		MaxIterations:  10,
		RebuildHistory: true,
	}
	stage := NewStageToolLoop(deps)
	turn := newRebuildTurn(request)

	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(stub.captured) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(stub.captured))
	}

	iter2 := stub.captured[1]
	if len(iter2) < 2 {
		t.Fatalf("iter2 too short (%d messages)", len(iter2))
	}

	needle := "<user_request>" + request + "</user_request>"
	if !strings.Contains(iter2[1].Content, needle) {
		t.Errorf("user request not preserved in rebuilt message\nwant substring: %q\ncontent: %s", needle, iter2[1].Content)
	}
}

// TestToolLoop_RebuildHistory_StepNumIncrements verifies that running three
// tool-call iterations produces a rebuilt message on the third LLM call that
// contains both <step_1> and <step_2>, confirming the step counter increments
// correctly across iterations.
func TestToolLoop_RebuildHistory_StepNumIncrements(t *testing.T) {
	stub := newStubChatter(2) // calls 1 and 2 → tool call; call 3 → done
	deps := ToolLoopDeps{
		ClientFactory:  func(*Turn) llm.Chatter { return stub },
		BuiltinHandler: echoBuiltin,
		MaxIterations:  10,
		RebuildHistory: true,
	}
	stage := NewStageToolLoop(deps)
	turn := newRebuildTurn("multi-step task")

	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(stub.captured) < 3 {
		t.Fatalf("expected at least 3 LLM calls, got %d", len(stub.captured))
	}

	iter3 := stub.captured[2]
	if len(iter3) < 2 {
		t.Fatalf("iter3 too short (%d messages)", len(iter3))
	}

	userContent := iter3[1].Content
	for _, tag := range []string{"<step_1>", "<step_2>"} {
		if !strings.Contains(userContent, tag) {
			t.Errorf("iter3 user message missing %q\ncontent: %s", tag, userContent)
		}
	}
}
