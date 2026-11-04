package agentcore

import (
	"context"
	"errors"
	"strings"
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
				case want != nil && got != nil:
					// If the test wanted ErrToolBlocked specifically, demand
					// the sentinel — anything else (including a wrapped one)
					// is wrong here. Otherwise compare error text exactly.
					if errors.Is(want, ErrToolBlocked) {
						if !errors.Is(got, ErrToolBlocked) {
							t.Errorf("tc[%d]: got %v, want ErrToolBlocked", i, got)
						}
					} else if want.Error() != got.Error() {
						t.Errorf("tc[%d]: got %v, want %v", i, got, want)
					}
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

// TestStagePolicyToolCall_DispositionSemantics pins the disposition of each
// policy outcome: a tool that is blocked (OPA or evaluator error) or is
// awaiting parent approval NEVER executed, so it must carry
// DispositionDenied / DispositionPending, not DispositionAttempted — which
// outcomeWarning would read as "some changes may have occurred" in later
// iterations of a multi-iteration turn.
func TestStagePolicyToolCall_DispositionSemantics(t *testing.T) {
	tests := []struct {
		name  string
		eval  *fakeToolEval
		tool  string
		wantD ToolDisposition
	}{
		{
			name:  "OPA block is denied, not attempted",
			tool:  "builtin__file_write",
			wantD: DispositionDenied,
			eval: &fakeToolEval{
				decisions: map[string]policy.ToolDecision{
					"file_write": {Allow: false, Action: "block", Reason: "not allowed"},
				},
			},
		},
		{
			name:  "evaluator error is denied, not attempted",
			tool:  "builtin__file_write",
			wantD: DispositionDenied,
			eval: &fakeToolEval{
				errs: map[string]error{"file_write": errors.New("opa unavailable")},
			},
		},
		{
			name:  "approval request is pending, not attempted",
			tool:  "builtin__browser_click",
			wantD: DispositionPending,
			eval: &fakeToolEval{
				decisions: map[string]policy.ToolDecision{
					"browser_click": {Allow: false, Action: "request_approval"},
				},
			},
		},
		{
			name:  "allowed call keeps DispositionAttempted (it will execute)",
			tool:  "builtin__web_fetch",
			wantD: DispositionAttempted,
			eval: &fakeToolEval{
				decisions: map[string]policy.ToolDecision{
					"web_fetch": {Allow: true},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turn := &Turn{
				User:      &config.UserConfig{Name: "alice", Role: "child", AgeGroup: "age_8_12"},
				ToolCalls: []ToolResult{{ToolName: tt.tool}},
			}
			if err := NewStagePolicyToolCall(tt.eval)(context.Background(), turn); err != nil {
				t.Fatalf("stage returned error: %v", err)
			}
			if got := turn.ToolCalls[0].Disposition; got != tt.wantD {
				t.Errorf("Disposition = %v, want %v", got, tt.wantD)
			}
		})
	}
}

// TestStagePolicyToolCall_MultiIterationOutcomeWarning is the end-to-end
// scenario from the bot finding: in a multi-iteration turn the iteration-1
// policy results (one OPA-blocked, one awaiting approval) persist in
// turn.ToolCalls; iteration 2's allowed tool executes successfully. The
// outcomeWarning computed over the accumulated results must not claim
// "changes may have occurred" for the tool that never ran, and must report
// the denial and the pending approval instead.
func TestStagePolicyToolCall_MultiIterationOutcomeWarning(t *testing.T) {
	// Iteration 1 batch: file_write (OPA-blocked) + browser_click
	// (approval requested). Run the policy stage over the placeholders.
	turn := &Turn{
		User: &config.UserConfig{Name: "alice", Role: "child", AgeGroup: "age_8_12"},
		ToolCalls: []ToolResult{
			{ToolName: "builtin__file_write"},
			{ToolName: "builtin__browser_click"},
		},
	}
	eval := &fakeToolEval{
		decisions: map[string]policy.ToolDecision{
			"file_write":    {Allow: false, Action: "block", Reason: "not allowed"},
			"browser_click": {Allow: false, Action: "request_approval"},
		},
	}
	if err := NewStagePolicyToolCall(eval)(context.Background(), turn); err != nil {
		t.Fatalf("stage returned error: %v", err)
	}
	// Iteration 2: the model's follow-up request is allowed and executes.
	// (Entries already carrying an error are skipped by the stage, so the
	// iteration-1 dispositions above persist unchanged.)
	turn.ToolCalls = append(turn.ToolCalls, ToolResult{
		ToolName:    "builtin__web_fetch",
		Output:      "URL: https://example.com\nbody",
		Outcome:     OutcomeSuccess,
		Disposition: DispositionAttempted,
	})
	registry := []Tool{
		{Name: "builtin__file_write"},
		{Name: "builtin__browser_click"},
		{Name: "builtin__web_fetch"},
	}

	warning := strings.ToLower(outcomeWarning(turn.ToolCalls, registry))
	for _, absent := range []string{"changes may have occurred", "uncertain"} {
		if strings.Contains(warning, absent) {
			t.Errorf("outcomeWarning for a never-run blocked/pending tool must not claim %q, got: %q", absent, warning)
		}
	}
	for _, want := range []string{"denied", "pending approval"} {
		if !strings.Contains(warning, want) {
			t.Errorf("outcomeWarning lacks %q, got: %q", want, warning)
		}
	}
}
