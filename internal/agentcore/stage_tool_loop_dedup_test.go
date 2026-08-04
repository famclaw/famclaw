package agentcore

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

// dedupChatter is a minimal llm.Chatter that returns a fixed sequence of
// responses to ChatWithTools and records (copies of) the message slices it
// is invoked with, so tests can assert on the corrective feedback fed back
// to the model.
type dedupChatter struct {
	mu        sync.Mutex
	callCount int
	responses []llm.Message
	captured  [][]llm.Message
}

func (c *dedupChatter) Chat(_ context.Context, _ []llm.Message, _ float64, _ int, _ func(string)) (string, error) {
	return "done", nil
}
func (c *dedupChatter) ChatMessage(_ context.Context, _ []llm.Message, _ float64, _ int) (*llm.Message, error) {
	return &llm.Message{Role: "assistant", Content: "done"}, nil
}
func (c *dedupChatter) ChatSync(_ context.Context, _ []llm.Message, _ float64, _ int) (string, error) {
	return "done", nil
}
func (c *dedupChatter) ChatWithTools(_ context.Context, msgs []llm.Message, _ float64, _ int, _ []llm.ToolDef) (*llm.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.callCount++
	snap := make([]llm.Message, len(msgs))
	copy(snap, msgs)
	c.captured = append(c.captured, snap)
	if len(c.responses) == 0 {
		return &llm.Message{Role: "assistant", Content: "done"}, nil
	}
	resp := c.responses[0]
	c.responses = c.responses[1:]
	return &resp, nil
}
func (c *dedupChatter) Ping(context.Context) error { return nil }

// TestStageToolLoop_DedupFailedCall verifies that when the model re-emits a
// tool call (same name + arguments) that already failed earlier in the turn,
// the loop short-circuits it with a corrective result instead of
// re-executing the same doomed call. Different args must still execute, and
// key-order differences in the args map must not defeat the match.
func TestStageToolLoop_DedupFailedCall(t *testing.T) {
	const toolName = "builtin__search"

	cases := []struct {
		name             string
		initialArgs      map[string]any // args the model emits on its first tool call
		retryArgs        map[string]any // args the model re-emits on its second LLM call
		wantHandlerCalls int            // how many times the always-failing handler runs
		wantShortCircuit bool           // whether a ErrToolAlreadyFailed result appears
		wantCorrective   bool           // whether the corrective message reaches the LLM transcript
	}{
		{
			name:             "identical retry is short-circuited",
			initialArgs:      map[string]any{"query": "cats"},
			retryArgs:        map[string]any{"query": "cats"},
			wantHandlerCalls: 1,
			wantShortCircuit: true,
			wantCorrective:   true,
		},
		{
			name:             "different args are re-executed (no short-circuit)",
			initialArgs:      map[string]any{"query": "cats"},
			retryArgs:        map[string]any{"query": "dogs"},
			wantHandlerCalls: 2,
			wantShortCircuit: false,
			wantCorrective:   false,
		},
		{
			name:             "key-order-invariant args still short-circuit",
			initialArgs:      map[string]any{"a": "1", "b": "2"},
			retryArgs:        map[string]any{"b": "2", "a": "1"},
			wantHandlerCalls: 1,
			wantShortCircuit: true,
			wantCorrective:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chatter := &dedupChatter{
				responses: []llm.Message{
					// 2nd LLM call: the model re-emits a (possibly identical) call
					{
						Role:    "assistant",
						Content: "retrying",
						ToolCalls: []llm.ToolCall{{
							ID:       "call_retry",
							Function: llm.ToolCallFunction{Name: toolName, Arguments: tc.retryArgs},
						}},
					},
					// 3rd LLM call: the model gives up (final answer, no tool call)
					{Role: "assistant", Content: "I couldn't find it."},
				},
			}

			var handlerCalls int
			deps := ToolLoopDeps{
				MaxIterations: 10,
				BuiltinHandler: func(_ context.Context, name string, _ map[string]any) (string, error) {
					handlerCalls++
					if name != toolName {
						return "", errors.New("unexpected tool: " + name)
					}
					return "", errors.New("simulated search failure")
				},
				ClientFactory: func(*Turn) llm.Chatter { return chatter },
			}

			turn := &Turn{
				User:  &config.UserConfig{Name: "tester", Role: "child"},
				Tools: []Tool{{Name: toolName}},
			}
			// The model's FIRST emission -- executed by the loop, then failed.
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{
				ID:       "call1",
				Function: llm.ToolCallFunction{Name: toolName, Arguments: tc.initialArgs},
			}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "search for cats"}})

			stage := NewStageToolLoop(deps)
			if err := stage(context.Background(), turn); err != nil {
				t.Fatalf("stage error: %v", err)
			}

			// 1) The handler must be invoked exactly wantHandlerCalls times.
			if handlerCalls != tc.wantHandlerCalls {
				t.Errorf("handler calls = %d, want %d", handlerCalls, tc.wantHandlerCalls)
			}

			// 2) Count short-circuited vs. original-failure results.
			var shortCircuited, originalFails int
			for _, tr := range turn.ToolCalls {
				if tr.Error == nil {
					continue
				}
				if errors.Is(tr.Error, ErrToolAlreadyFailed) {
					shortCircuited++
				} else {
					originalFails++
				}
			}
			if tc.wantShortCircuit {
				if shortCircuited != 1 {
					t.Errorf("expected 1 short-circuited (ErrToolAlreadyFailed) result, got %d; turn.ToolCalls=%+v", shortCircuited, turn.ToolCalls)
				}
			} else if shortCircuited != 0 {
				t.Errorf("expected 0 short-circuited results, got %d", shortCircuited)
			}
			if !tc.wantShortCircuit && originalFails != tc.wantHandlerCalls {
				t.Errorf("expected %d original failures, got %d", tc.wantHandlerCalls, originalFails)
			}

			// 3) The corrective message must be fed back to the model in the
			// transcript of the LLM call that follows the (short-circuited)
			// retry. captured[1] is that call.
			if len(chatter.captured) < 2 {
				t.Fatalf("expected at least 2 LLM calls, got %d", len(chatter.captured))
			}
			iter2Msgs := chatter.captured[1]
			correctiveSeen := false
			for _, m := range iter2Msgs {
				if m.Role == "tool" && strings.Contains(m.Content, "already failed earlier in this turn") {
					correctiveSeen = true
				}
			}
			if tc.wantCorrective && !correctiveSeen {
				t.Errorf("expected corrective message in LLM transcript after retry; captured:\n%+v", iter2Msgs)
			}
			if !tc.wantCorrective && correctiveSeen {
				t.Errorf("corrective message should not appear for different args; captured:\n%+v", iter2Msgs)
			}
		})
	}
}

// TestStageToolLoop_DedupDoesNotBlockSuccessfulCall verifies that a call
// which SUCCEEDED earlier in the turn is NOT short-circuited when the model
// re-emits it: only FAILED calls are deduped.
func TestStageToolLoop_DedupDoesNotBlockSuccessfulCall(t *testing.T) {
	const toolName = "builtin__echo"
	chatter := &dedupChatter{
		responses: []llm.Message{
			// 2nd LLM call: model re-emits the same (successful) call
			{
				Role:    "assistant",
				Content: "again",
				ToolCalls: []llm.ToolCall{{
					ID:       "call2",
					Function: llm.ToolCallFunction{Name: toolName, Arguments: map[string]any{"n": "1"}},
				}},
			},
			// 3rd LLM call: final answer
			{Role: "assistant", Content: "done now"},
		},
	}

	var handlerCalls int
	deps := ToolLoopDeps{
		MaxIterations: 10,
		BuiltinHandler: func(_ context.Context, name string, _ map[string]any) (string, error) {
			handlerCalls++
			return "ok", nil // always succeeds
		},
		ClientFactory: func(*Turn) llm.Chatter { return chatter },
	}

	turn := &Turn{
		User:  &config.UserConfig{Name: "tester", Role: "child"},
		Tools: []Tool{{Name: toolName}},
	}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{{
		ID:       "call1",
		Function: llm.ToolCallFunction{Name: toolName, Arguments: map[string]any{"n": "1"}},
	}})
	turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "echo"}})

	stage := NewStageToolLoop(deps)
	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	// Both calls executed (no short-circuit) because the first SUCCEEDED.
	if handlerCalls != 2 {
		t.Errorf("handler calls = %d, want 2 (successful calls are not deduped)", handlerCalls)
	}
	// No ErrToolAlreadyFailed result should be present.
	for _, tr := range turn.ToolCalls {
		if errors.Is(tr.Error, ErrToolAlreadyFailed) {
			t.Errorf("successful call should not be short-circuited: %+v", tr)
		}
	}
}
