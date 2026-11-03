package agentcore

import (
	"strings"
	"testing"
)

func TestOutcomeWarningStates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		calls  []ToolResult
		want   []string
		absent []string
	}{
		{"all success", []ToolResult{{Disposition: DispositionAttempted, Outcome: OutcomeSuccess}}, nil, []string{"fail", "uncertain"}},
		{"success and failure", []ToolResult{{Disposition: DispositionAttempted, Outcome: OutcomeSuccess}, {Disposition: DispositionAttempted, Outcome: OutcomeFailure}}, []string{"succeeded", "failure"}, nil},
		{"failure and unknown", []ToolResult{{Disposition: DispositionAttempted, Outcome: OutcomeFailure}, {Disposition: DispositionAttempted, Outcome: OutcomeUnknown}}, []string{"failure", "uncertain"}, nil},
		{"all three", []ToolResult{{Disposition: DispositionAttempted, Outcome: OutcomeSuccess}, {Disposition: DispositionAttempted, Outcome: OutcomeFailure}, {Disposition: DispositionAttempted, Outcome: OutcomeUnknown}}, []string{"succeeded", "failure", "uncertain"}, nil},
		{"policy and dedup", []ToolResult{{Disposition: DispositionDenied}, {Disposition: DispositionPending}, {Disposition: DispositionSkipped}}, []string{"denied", "pending", "skipped"}, []string{"failure", "succeeded"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := strings.ToLower(outcomeWarning(tc.calls))
			for _, w := range tc.want {
				if !strings.Contains(s, w) {
					t.Errorf("warning lacks %q", w)
				}
			}
			for _, w := range tc.absent {
				if strings.Contains(s, w) {
					t.Errorf("warning incorrectly contains %q", w)
				}
			}
		})
	}
}
