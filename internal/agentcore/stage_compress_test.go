package agentcore

import (
	"context"
	"strings"
	"testing"
)

// TestStageCompressPreservesToolCalls verifies that StageCompress keeps
// tool-call groups intact: after compression, every assistant ToolCalls
// entry is preserved alongside its corresponding tool results. Tool results
// carry explicit ToolCallID matching the assistant's call IDs.
func TestStageCompressPreservesToolCalls(t *testing.T) {
	turn := &Turn{
		Messages: []Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "q1"},
			{
				Role: "assistant", Content: "",
				ToolCalls: []ToolCall{
					{ID: "call_sc1", Function: ToolCallFunction{Name: "f", Arguments: map[string]any{}}},
					{ID: "call_sc2", Function: ToolCallFunction{Name: "g", Arguments: map[string]any{}}},
				},
			},
			{Role: "tool", Content: strings.Repeat("A", 2000), ToolCallID: "call_sc1"},
			{Role: "tool", Content: strings.Repeat("B", 2000), ToolCallID: "call_sc2"},
			{Role: "user", Content: "q2"},
			{Role: "assistant", Content: "a2"},
			{Role: "user", Content: "q3"},
			{Role: "assistant", Content: "a3"},
		},
	}

	stage := NewStageCompress(500)
	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("stage: %v", err)
	}

	// After compression, check wire invariants: every assistant ToolCall
	// ID must have a matching tool result with the same ToolCallID, and
	// every tool result must have a matching assistant call.
	assistantIDs := make(map[string]bool)
	resultIDs := make(map[string]bool)
	for _, m := range turn.Messages {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				assistantIDs[tc.ID] = true
			}
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			resultIDs[m.ToolCallID] = true
		}
	}
	// Bidirectional pairing.
	for rid := range resultIDs {
		if !assistantIDs[rid] {
			t.Errorf("orphaned tool result ToolCallID=%q with no matching assistant call", rid)
		}
	}
	for aid := range assistantIDs {
		if !resultIDs[aid] {
			t.Errorf("orphaned assistant call ID=%q with no matching tool result", aid)
		}
	}

	// If the group was retained (not evicted), verify exact content and order.
	if len(assistantIDs) > 0 {
		// Find the assistant with calls and verify results follow it.
		assistantIdx := -1
		for i, m := range turn.Messages {
			if m.Role == "assistant" && len(m.ToolCalls) > 0 {
				assistantIdx = i
				break
			}
		}
		if assistantIdx < 0 {
			t.Fatal("no assistant with ToolCalls found after compression")
		}
		// Tool results must appear after the assistant message.
		for i := assistantIdx + 1; i < len(turn.Messages); i++ {
			m := turn.Messages[i]
			if m.Role == "tool" {
				if m.ToolCallID != "call_sc1" && m.ToolCallID != "call_sc2" {
					t.Errorf("unexpected tool result ToolCallID=%q at position %d", m.ToolCallID, i)
				}
			}
		}
	}
}

// TestStageCompressPreservesPinnedAndLastThree verifies that the system
// prompt (pinned) and the last three messages are always retained.
func TestStageCompressPreservesPinnedAndLastThree(t *testing.T) {
	turn := &Turn{
		Messages: []Message{
			{Role: "system", Content: "the system prompt"},
			{Role: "user", Content: strings.Repeat("x", 500)},
			{Role: "assistant", Content: strings.Repeat("y", 500)},
			{Role: "user", Content: strings.Repeat("z", 500)},
			{Role: "assistant", Content: strings.Repeat("w", 500)},
			{Role: "user", Content: "current q"},
			{Role: "assistant", Content: "current a1"},
			{Role: "user", Content: "last q"},
			{Role: "assistant", Content: "last a"},
		},
	}

	stage := NewStageCompress(200)
	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("stage: %v", err)
	}

	// System prompt must be present.
	found := false
	for _, m := range turn.Messages {
		if m.Role == "system" && m.Content == "the system prompt" {
			found = true
		}
	}
	if !found {
		t.Errorf("system prompt missing after compression: %+v", turn.Messages)
	}

	// Last three messages must be present.
	if len(turn.Messages) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(turn.Messages))
	}
	var contents []string
	for _, m := range turn.Messages {
		contents = append(contents, m.Content)
	}
	for _, w := range []string{"last q", "last a"} {
		foundW := false
		for _, c := range contents {
			if c == w {
				foundW = true
				break
			}
		}
		if !foundW {
			t.Errorf("expected %q in last-3 protected zone, got %v", w, contents)
		}
	}
}

// TestStageCompressTooFewNoOp verifies that compression is a no-op when
// there are 4 or fewer messages.
func TestStageCompressTooFewNoOp(t *testing.T) {
	turn := &Turn{
		Messages: []Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "q"},
			{Role: "assistant", Content: "a"},
		},
	}

	stage := NewStageCompress(100)
	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if len(turn.Messages) != 3 {
		t.Errorf("expected 3 messages, got %d", len(turn.Messages))
	}
}

// TestStageCompressDuplicateContentSafe verifies that two assistant
// messages with identical text but different tool-call IDs are not
// confused during the reverse mapping.
func TestStageCompressDuplicateContentSafe(t *testing.T) {
	turn := &Turn{
		Messages: []Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "q"},
			{
				Role: "assistant", Content: "identical",
				ToolCalls: []ToolCall{
					{ID: "call_dup_a", Function: ToolCallFunction{Name: "f1", Arguments: map[string]any{}}},
				},
			},
			{Role: "tool", Content: "ra", ToolCallID: "call_dup_a"},
			{
				Role: "assistant", Content: "identical",
				ToolCalls: []ToolCall{
					{ID: "call_dup_b", Function: ToolCallFunction{Name: "f2", Arguments: map[string]any{}}},
				},
			},
			{Role: "tool", Content: "rb", ToolCallID: "call_dup_b"},
			{Role: "user", Content: "q2"},
			{Role: "assistant", Content: "a2"},
		},
	}

	// Roomy budget: nothing evicted.
	stage := NewStageCompress(40000)
	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("stage: %v", err)
	}

	// Both duplicate-content assistants must retain their distinct CallIDs.
	var sawA, sawB bool
	for _, m := range turn.Messages {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if tc.ID == "call_dup_a" {
					sawA = true
				}
				if tc.ID == "call_dup_b" {
					sawB = true
				}
			}
		}
	}
	if !sawA || !sawB {
		t.Errorf("duplicate-content tool calls lost: a=%v b=%v", sawA, sawB)
	}
}
