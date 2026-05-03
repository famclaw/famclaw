package agentcore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/policy"
)

// fakeToolEval lets a test pin the decision returned for each ToolName.
type fakeToolEval struct {
	decisions map[string]policy.ToolDecision
	errs      map[string]error
	calls     []policy.ToolCallInput
}

func (f *fakeToolEval) EvaluateToolCall(_ context.Context, in policy.ToolCallInput) (policy.ToolDecision, error) {
	f.calls = append(f.calls, in)
	if err, ok := f.errs[in.ToolName]; ok {
		return policy.ToolDecision{}, err
	}
	if d, ok := f.decisions[in.ToolName]; ok {
		return d, nil
	}
	return policy.ToolDecision{Allow: true}, nil
}

func TestStagePolicyToolCall(t *testing.T) {
	tests := []struct {
		name      string
		eval      *fakeToolEval
		toolCalls []ToolResult
		wantErrs  []error // expected ToolCalls[i].Error after stage
	}{
		{
			name: "allow leaves Error nil",
			eval: &fakeToolEval{
				decisions: map[string]policy.ToolDecision{
					"web_fetch": {Allow: true},
				},
			},
			toolCalls: []ToolResult{{ToolName: "builtin__web_fetch"}},
			wantErrs:  []error{nil},
		},
		{
			name: "deny sets ErrToolBlocked on the slice element",
			eval: &fakeToolEval{
				decisions: map[string]policy.ToolDecision{
					"web_fetch": {Allow: false, Reason: "not for your age group"},
				},
			},
			toolCalls: []ToolResult{{ToolName: "builtin__web_fetch"}},
			wantErrs:  []error{ErrToolBlocked},
		},
		{
			name: "evaluator error treated as block",
			eval: &fakeToolEval{
				errs: map[string]error{"web_fetch": errors.New("boom")},
			},
			toolCalls: []ToolResult{{ToolName: "builtin__web_fetch"}},
			wantErrs:  []error{ErrToolBlocked},
		},
		{
			name: "pre-existing error skipped",
			eval: &fakeToolEval{
				decisions: map[string]policy.ToolDecision{
					"web_fetch": {Allow: false},
				},
			},
			toolCalls: []ToolResult{{ToolName: "builtin__web_fetch", Error: errors.New("earlier failure"), Duration: time.Millisecond}},
			wantErrs:  []error{errors.New("earlier failure")},
		},
		{
			name: "mixed batch — allow, deny, error all persist",
			eval: &fakeToolEval{
				decisions: map[string]policy.ToolDecision{
					"web_fetch":   {Allow: false},
					"spawn_agent": {Allow: true},
				},
				errs: map[string]error{"web_search": errors.New("eval boom")},
			},
			toolCalls: []ToolResult{
				{ToolName: "builtin__web_fetch"},
				{ToolName: "builtin__spawn_agent"},
				{ToolName: "mcp__search__web_search"},
			},
			wantErrs: []error{ErrToolBlocked, nil, ErrToolBlocked},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turn := &Turn{
				User:      &config.UserConfig{Name: "alice", Role: "child", AgeGroup: "age_8_12"},
				ToolCalls: tt.toolCalls,
			}
			s := NewStagePolicyToolCall(tt.eval)
			if err := s(context.Background(), turn); err != nil {
				t.Fatalf("stage returned error: %v", err)
			}
			if len(turn.ToolCalls) != len(tt.wantErrs) {
				t.Fatalf("len mismatch: got %d, want %d", len(turn.ToolCalls), len(tt.wantErrs))
			}
			for i, want := range tt.wantErrs {
				got := turn.ToolCalls[i].Error
				switch {
				case want == nil && got != nil:
					t.Errorf("tc[%d]: got error %v, want nil", i, got)
				case want != nil && got == nil:
					t.Errorf("tc[%d]: got nil, want error %v", i, want)
				case want != nil && got != nil && want.Error() != got.Error() && !errors.Is(got, ErrToolBlocked):
					t.Errorf("tc[%d]: got %v, want %v", i, got, want)
				}
			}
		})
	}
}

func TestStagePolicyToolCall_NilEvalIsNoop(t *testing.T) {
	stage := NewStagePolicyToolCall(nil)
	turn := &Turn{
		User:      &config.UserConfig{Name: "alice", Role: "child"},
		ToolCalls: []ToolResult{{ToolName: "builtin__web_fetch"}},
	}
	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("nil-eval stage should be a no-op, got: %v", err)
	}
	if turn.ToolCalls[0].Error != nil {
		t.Errorf("nil-eval should not block, got Error=%v", turn.ToolCalls[0].Error)
	}
}
