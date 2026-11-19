package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMarshalMessageToolCallGroupIntegrity verifies that when a Message
// slice containing a complete tool-call group is serialized to JSON (the
// wire format), the group is preserved as a well-formed unit: the
// assistant message carries its tool_calls and each role:"tool" result
// references a tool_call_id that matches one of those calls.
func TestMarshalMessageToolCallGroupIntegrity(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q"},
		{
			Role: "assistant", Content: "",
			ToolCalls: []ToolCall{
				{ID: "call_w1", Function: ToolCallFunction{Name: "f1", Arguments: ToolCallArguments{"x": 1}}},
				{ID: "call_w2", Function: ToolCallFunction{Name: "f2", Arguments: ToolCallArguments{"y": 2}}},
			},
		},
		{Role: "tool", Content: "r1", ToolCallID: "call_w1"},
		{Role: "tool", Content: "r2", ToolCallID: "call_w2"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
	}

	data, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Round-trip: decode the JSON back and verify group integrity.
	var back []Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(back) != len(msgs) {
		t.Fatalf("expected %d messages, got %d", len(msgs), len(back))
	}

	// Find the assistant message with tool_calls.
	var assistantMsg *Message
	for i := range back {
		if back[i].Role == "assistant" && len(back[i].ToolCalls) > 0 {
			assistantMsg = &back[i]
			break
		}
	}
	if assistantMsg == nil {
		t.Fatalf("no assistant message with tool_calls found in round-trip")
	}
	if len(assistantMsg.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(assistantMsg.ToolCalls))
	}
	if assistantMsg.ToolCalls[0].ID != "call_w1" || assistantMsg.ToolCalls[1].ID != "call_w2" {
		t.Errorf("tool call IDs not preserved: %q, %q", assistantMsg.ToolCalls[0].ID, assistantMsg.ToolCalls[1].ID)
	}

	// Verify the tool results reference the correct IDs.
	callIDSet := make(map[string]bool)
	for _, tc := range assistantMsg.ToolCalls {
		callIDSet[tc.ID] = true
	}
	var toolIDs []string
	for _, m := range back {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolIDs = append(toolIDs, m.ToolCallID)
			if !callIDSet[m.ToolCallID] {
				t.Errorf("tool result %q has no matching assistant call", m.ToolCallID)
			}
		}
	}
	if len(toolIDs) != 2 {
		t.Errorf("expected 2 tool results, got %d: %v", len(toolIDs), toolIDs)
	}
}

// TestMarshalMessageOversizedArguments verifies that a tool call with a
// large arguments payload is correctly serialized and deserialized
// without data loss.
func TestMarshalMessageOversizedArguments(t *testing.T) {
	bigArg := strings.Repeat("x", 10000)
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q"},
		{
			Role: "assistant", Content: "",
			ToolCalls: []ToolCall{
				{ID: "call_big", Function: ToolCallFunction{Name: "f", Arguments: ToolCallArguments{"data": bigArg}}},
			},
		},
		{Role: "tool", Content: "ok", ToolCallID: "call_big"},
	}

	data, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var back []Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Verify the large argument survived the round-trip.
	for _, m := range back {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if tc.ID == "call_big" {
					arg, ok := tc.Function.Arguments["data"]
					if !ok {
						t.Errorf("argument 'data' missing after round-trip")
					} else {
						s, _ := arg.(string)
						if len(s) != 10000 {
							t.Errorf("argument length: got %d, want 10000", len(s))
						}
					}
				}
			}
		}
	}
}
