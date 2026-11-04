package agentcore

import (
	"strings"
	"testing"
)

func TestOutcomeWarningStates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		calls  []ToolResult
		tools  []Tool
		want   []string
		absent []string
	}{
		{"all success", []ToolResult{{Disposition: DispositionAttempted, Outcome: OutcomeSuccess}}, nil, nil, []string{"fail", "uncertain"}},
		{"success and failure", []ToolResult{{Disposition: DispositionAttempted, Outcome: OutcomeSuccess}, {Disposition: DispositionAttempted, Outcome: OutcomeFailure}}, nil, []string{"succeeded", "failure"}, nil},
		{"failure and unknown", []ToolResult{{Disposition: DispositionAttempted, Outcome: OutcomeFailure}, {Disposition: DispositionAttempted, Outcome: OutcomeUnknown}}, nil, []string{"failure", "uncertain"}, nil},
		{"all three", []ToolResult{{Disposition: DispositionAttempted, Outcome: OutcomeSuccess}, {Disposition: DispositionAttempted, Outcome: OutcomeFailure}, {Disposition: DispositionAttempted, Outcome: OutcomeUnknown}}, nil, []string{"succeeded", "failure", "uncertain"}, nil},
		{"policy and dedup", []ToolResult{{Disposition: DispositionDenied}, {Disposition: DispositionPending}, {Disposition: DispositionSkipped}}, nil, []string{"denied", "pending", "skipped"}, []string{"failure", "succeeded"}},
		// Read-only scoping: a rate-limited web_search (or any tool declared
		// ReadOnly in the registry) must not emit the "changes may have
		// occurred" mutation warning.
		{
			"read-only failure",
			[]ToolResult{{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeFailure}},
			[]Tool{{Name: "builtin__web_search", ReadOnly: true}},
			[]string{"failure", "no changes were made"},
			[]string{"changes may"},
		},
		{
			"read-only failure via bare name",
			[]ToolResult{{ToolName: "web_search", Disposition: DispositionAttempted, Outcome: OutcomeFailure}},
			[]Tool{{Name: "builtin__web_search", ReadOnly: true}},
			[]string{"failure", "no changes were made"},
			[]string{"changes may"},
		},
		{
			"unmarked failure stays conservative",
			[]ToolResult{{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeFailure}},
			[]Tool{{Name: "builtin__web_search"}},
			[]string{"failure", "changes may still have occurred"},
			[]string{"no changes were made"},
		},
		{
			"mutation failure still warns",
			[]ToolResult{{ToolName: "builtin__file_write", Disposition: DispositionAttempted, Outcome: OutcomeFailure}},
			[]Tool{{Name: "builtin__file_write"}},
			[]string{"failure", "changes may still have occurred"},
			[]string{"no changes were made"},
		},
		{
			"mixed read-only and mutation failure",
			[]ToolResult{
				{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
				{ToolName: "builtin__file_write", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
			},
			[]Tool{{Name: "builtin__web_search", ReadOnly: true}, {Name: "builtin__file_write"}},
			[]string{"failure", "changes may still have occurred"},
			[]string{"no changes were made"},
		},
		{
			"read-only failure with success",
			[]ToolResult{
				{ToolName: "builtin__web_fetch", Disposition: DispositionAttempted, Outcome: OutcomeSuccess},
				{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
			},
			[]Tool{{Name: "builtin__web_fetch", ReadOnly: true}, {Name: "builtin__web_search", ReadOnly: true}},
			[]string{"succeeded", "no changes were made"},
			[]string{"changes may"},
		},
		{
			"read-only unknown",
			[]ToolResult{{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeUnknown}},
			[]Tool{{Name: "builtin__web_search", ReadOnly: true}},
			[]string{"uncertain", "no changes were made"},
			[]string{"changes may"},
		},
		{
			"mutation unknown still warns",
			[]ToolResult{{ToolName: "mcp__android__tap", Disposition: DispositionAttempted, Outcome: OutcomeUnknown}},
			[]Tool{{Name: "mcp__android__tap"}},
			[]string{"uncertain", "changes may"},
			[]string{"no changes were made"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := strings.ToLower(outcomeWarning(tc.calls, tc.tools))
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
