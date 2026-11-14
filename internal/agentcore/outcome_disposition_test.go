package agentcore

import (
	"context"
	"github.com/famclaw/famclaw/internal/policy"
	"strings"
	"testing"
)

func TestOutcomeDispositionFinalAnswer(t *testing.T) {
	for _, tc := range []struct {
		name, action, want string
		called             bool
	}{{"denied", "block", "denied", false}, {"pending", "request_approval", "pending", false}, {"success", "allow", "", true}} {
		t.Run(tc.name, func(t *testing.T) {
			eval := &approvalToolEval{decisions: map[string]policy.ToolDecision{"file_write": {Action: tc.action, Allow: tc.called}}}
			deps, called := makeApprovalDeps(t, eval, &fakeApprovalStore{})
			turn := makeApprovalTurn("child", "child", "file_write")
			if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if *called != tc.called {
				t.Error("wrong execution disposition")
			}
			if tc.want != "" && !strings.Contains(strings.ToLower(turn.Output), tc.want) {
				t.Errorf("final answer omitted %s status", tc.want)
			}
			if tc.want != "" && strings.Contains(strings.ToLower(turn.Output), "tool reported a failure") {
				t.Error("not-run call claimed as execution failure")
			}
			if tc.called && turn.Output != "done" {
				t.Error("success answer changed")
			}
		})
	}
}
