package agentcore

import (
	"strings"
	"testing"
)

func TestReadOnlyOutcomeDoesNotAssertUnverifiedEffects(t *testing.T) {
	registry := []Tool{{Name: "write"}, {Name: "lookup", ReadOnly: true}}
	for _, tc := range []struct {
		name  string
		calls []ToolResult
		want  []string
	}{
		{
			name: "successful mutation and uncertain lookup",
			calls: []ToolResult{
				{ToolName: "write", Disposition: DispositionAttempted, Outcome: OutcomeSuccess},
				{ToolName: "lookup", Disposition: DispositionAttempted, Outcome: OutcomeUnknown},
			},
			want: []string{"succeeded", "marked read-only", "uncertain"},
		},
		{
			name: "both mutations and lookup fail",
			calls: []ToolResult{
				{ToolName: "write", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
				{ToolName: "lookup", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
			},
			want: []string{"changes may", "marked read-only", "failure"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.ToLower(outcomeWarning(tc.calls, registry))
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("warning %q lacks %q", got, want)
				}
			}
			if strings.Contains(got, "no changes") {
				t.Errorf("warning asserts unverified absence of changes: %q", got)
			}
		})
	}
}
