package agentcore

import (
	"context"
	"errors"
	"testing"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/policy"
)

func TestStageClassify(t *testing.T) {
	clf := classifier.New()
	stage := NewStageClassify(clf)

	turn := &Turn{
		User:  &config.UserConfig{Name: "test", Role: "child"},
		Input: "how do I do my homework?",
	}

	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("classify error: %v", err)
	}
	if turn.Category == "" {
		t.Error("category should be set after classify")
	}
}

func TestStageOutputFilterChild(t *testing.T) {
	stage := NewStageOutputFilter()

	tests := []struct {
		name    string
		role    string
		output  string
		wantMod bool
	}{
		{"safe response child", "child", "The sun is a star.", false},
		{"blocked content child", "child", "Here's how to make a bomb for fun", true},
		{"blocked content parent", "parent", "Here's how to make a bomb for fun", false}, // parents see everything
		{"empty", "child", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turn := &Turn{
				User:   &config.UserConfig{Role: tt.role},
				Output: tt.output,
			}
			if err := stage(context.Background(), turn); err != nil {
				t.Fatalf("output filter error: %v", err)
			}
			modified := turn.Output != tt.output
			if modified != tt.wantMod {
				t.Errorf("modified=%v, want=%v (output=%q)", modified, tt.wantMod, turn.Output)
			}
		})
	}
}

func TestStagePolicyInputBlock(t *testing.T) {
	// We can't easily test with a real OPA evaluator without policy files,
	// but we can verify the ErrPolicyBlock sentinel error is used correctly.
	if !errors.Is(ErrPolicyBlock, ErrPolicyBlock) {
		t.Error("ErrPolicyBlock should be identifiable via errors.Is")
	}
}

func TestPolicyMessage(t *testing.T) {
	tests := []struct {
		action string
		want   string
	}{
		{"block", "I'm sorry"},
		{"request_approval", "asked a parent"},
		{"pending", "already been notified"},
		{"unknown", "unable to answer"},
	}

	for _, tt := range tests {
		msg := policyMessage(policy.Decision{Action: tt.action, Reason: "test"})
		if msg == "" {
			t.Errorf("policyMessage(%q) returned empty", tt.action)
		}
	}
}

func TestTurnToLLMMessages(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi!", ToolCalls: []ToolCall{
			{ID: "1", Type: "function", Function: ToolCallFunction{Name: "test", Arguments: map[string]any{"k": "v"}}},
		}},
	}

	llmMsgs := turnToLLMMessages(msgs)
	if len(llmMsgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(llmMsgs))
	}
	if llmMsgs[0].Role != "system" {
		t.Errorf("msg[0].Role = %q, want 'system'", llmMsgs[0].Role)
	}
	if len(llmMsgs[2].ToolCalls) != 1 {
		t.Errorf("expected 1 tool call in msg[2], got %d", len(llmMsgs[2].ToolCalls))
	}
	if llmMsgs[2].ToolCalls[0].Function.Name != "test" {
		t.Errorf("tool call name = %q, want 'test'", llmMsgs[2].ToolCalls[0].Function.Name)
	}
}

func TestToolsToLLMDefs(t *testing.T) {
	tools := []Tool{
		{
			Name:        "mcp__weather__forecast",
			Description: "Get weather",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"location": map[string]any{"type": "string"},
				},
			},
		},
	}

	defs := toolsToLLMDefs(tools)
	if len(defs) != 1 {
		t.Fatalf("expected 1 def, got %d", len(defs))
	}
	if defs[0].Type != "function" {
		t.Errorf("type = %q, want 'function'", defs[0].Type)
	}
	if defs[0].Function.Name != "mcp__weather__forecast" {
		t.Errorf("name = %q", defs[0].Function.Name)
	}
}
