package agentcore

import (
	"strings"
	"testing"
)

// TestOutcomeWarning_BothFailureCategories is the regression test for the
// co-occurring attempted-category gap: a turn containing both a mutating
// tool failure and a read-only tool failure must mention BOTH categories
// (previously the else-if chain emitted only the mutation note and silently
// dropped the read-only one), with the more important mutation note first.
func TestOutcomeWarning_BothFailureCategories(t *testing.T) {
	registry := []Tool{
		{Name: "builtin__web_search", ReadOnly: true},
		{Name: "builtin__file_write"},
	}
	calls := []ToolResult{
		{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
		{ToolName: "builtin__file_write", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
	}
	s := strings.ToLower(outcomeWarning(calls, registry))
	const mutNote = "some changes may still have occurred"
	const roNote = "marked read-only"
	if iMut, iRO := strings.Index(s, mutNote), strings.Index(s, roNote); iMut < 0 || iRO < 0 {
		t.Fatalf("warning must contain both the mutation note (%q) and the read-only note (%q), got: %q", mutNote, roNote, s)
	}
	if iMut, iRO := strings.Index(s, mutNote), strings.Index(s, roNote); iMut > iRO {
		t.Errorf("mutation note must come before the read-only note, got: %q", s)
	}
	// Same assertion with a co-occurring success in the turn: both notes are
	// re-worded for the success case, so check the success-qualified forms.
	calls = append([]ToolResult{{ToolName: "builtin__web_fetch", Disposition: DispositionAttempted, Outcome: OutcomeSuccess}}, calls...)
	s = strings.ToLower(outcomeWarning(calls, registry))
	const mutNoteSuccess = "another reported a failure; some changes may have occurred"
	const roNoteSuccess = "a tool marked read-only reported a failure"
	if iMut, iRO := strings.Index(s, mutNoteSuccess), strings.Index(s, roNoteSuccess); iMut < 0 || iRO < 0 {
		t.Errorf("with a success present the warning must still contain both notes, got: %q", s)
	}
}

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
			[]string{"failure", "marked read-only"},
			[]string{"changes may"},
		},
		{
			"read-only failure via bare name",
			[]ToolResult{{ToolName: "web_search", Disposition: DispositionAttempted, Outcome: OutcomeFailure}},
			[]Tool{{Name: "builtin__web_search", ReadOnly: true}},
			[]string{"failure", "marked read-only"},
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
			[]string{"failure", "changes may still have occurred", "marked read-only"},
			nil,
		},
		{
			"mixed mutation and read-only unknown",
			[]ToolResult{
				{ToolName: "builtin__file_write", Disposition: DispositionAttempted, Outcome: OutcomeUnknown},
				{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeUnknown},
			},
			[]Tool{{Name: "builtin__web_search", ReadOnly: true}, {Name: "builtin__file_write"}},
			[]string{"changes may have occurred", "marked read-only"},
			nil,
		},
		{
			"read-only failure with success",
			[]ToolResult{
				{ToolName: "builtin__web_fetch", Disposition: DispositionAttempted, Outcome: OutcomeSuccess},
				{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
			},
			[]Tool{{Name: "builtin__web_fetch", ReadOnly: true}, {Name: "builtin__web_search", ReadOnly: true}},
			[]string{"succeeded", "marked read-only"},
			[]string{"changes may"},
		},
		{
			"read-only unknown",
			[]ToolResult{{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeUnknown}},
			[]Tool{{Name: "builtin__web_search", ReadOnly: true}},
			[]string{"uncertain", "marked read-only"},
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
