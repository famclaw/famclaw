package browser

import (
	"strings"
	"testing"
)

// TestDoDone_RejectsEmptySummary verifies that an empty summary returns an
// error that mentions "summary". doDone does not touch the page so a
// zero-value Pool is sufficient.
func TestDoDone_RejectsEmptySummary(t *testing.T) {
	cases := []struct {
		name    string
		summary string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
	}
	var p Pool
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.doDone(map[string]any{"summary": tc.summary})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "summary") {
				t.Errorf("error %q does not mention 'summary'", err.Error())
			}
		})
	}
}

// TestDoDone_ReturnsSummaryVerbatim verifies that a non-empty summary is
// returned unchanged as the tool reply text.
func TestDoDone_ReturnsSummaryVerbatim(t *testing.T) {
	cases := []struct {
		name    string
		summary string
	}{
		{"simple", "hello world"},
		{"multiline", "line 1\nline 2"},
		{"leading space preserved", "  indented"},
	}
	var p Pool
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := p.doDone(map[string]any{"summary": tc.summary})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.summary {
				t.Errorf("got %q, want %q", got, tc.summary)
			}
		})
	}
}

// TestDoFillForm_RejectsEmptyFields verifies that doFillForm returns an error
// mentioning "fields" when the fields array is nil or empty. Validation
// happens before any page interaction, so a zero-value Pool with a minimal
// session stub is enough.
func TestDoFillForm_RejectsEmptyFields(t *testing.T) {
	cases := []struct {
		name   string
		fields any
	}{
		{"nil", nil},
		{"empty slice", []any{}},
	}
	var p Pool
	sess := &userSession{refs: make(map[string]RefEntry)}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.doFillForm(sess, map[string]any{"fields": tc.fields})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "fields") {
				t.Errorf("error %q does not mention 'fields'", err.Error())
			}
		})
	}
}

// TestDoFillForm_RejectsMissingRef verifies that a field entry without a
// "ref" key returns an error mentioning "ref".
func TestDoFillForm_RejectsMissingRef(t *testing.T) {
	var p Pool
	sess := &userSession{refs: make(map[string]RefEntry)}
	fields := []any{map[string]any{"value": "x"}}
	_, err := p.doFillForm(sess, map[string]any{"fields": fields})
	if err == nil {
		t.Fatal("expected error for missing ref, got nil")
	}
	if !strings.Contains(err.Error(), "ref") {
		t.Errorf("error %q does not mention 'ref'", err.Error())
	}
}

// TestDoFillForm_RejectsMissingValue verifies that a field entry without a
// "value" key returns an error mentioning "value".
func TestDoFillForm_RejectsMissingValue(t *testing.T) {
	var p Pool
	sess := &userSession{refs: make(map[string]RefEntry)}
	fields := []any{map[string]any{"ref": "e1"}}
	_, err := p.doFillForm(sess, map[string]any{"fields": fields})
	if err == nil {
		t.Fatal("expected error for missing value, got nil")
	}
	if !strings.Contains(err.Error(), "value") {
		t.Errorf("error %q does not mention 'value'", err.Error())
	}
}
