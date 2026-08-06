package reminder

import (
	"testing"
	"time"
)

// TestParseTimeNaturalLanguage covers the phrases a person actually types when
// setting a reminder. It reproduces the captain's failures ("in 1 min",
// "in 20 seconds") on the old parser and must pass once the parser accepts
// relative durations and common abbreviations.
func TestParseTimeNaturalLanguage(t *testing.T) {
	base := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	tomorrowMorning := time.Date(2026, 1, 16, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		input    string
		expected time.Duration // delta from base
	}{
		{"in 1 min", "in 1 min", 1 * time.Minute},
		{"in 2 mins", "in 2 mins", 2 * time.Minute},
		{"in 20 seconds", "in 20 seconds", 20 * time.Second},
		{"in 5 minutes", "in 5 minutes", 5 * time.Minute},
		{"in an hour", "in an hour", 1 * time.Hour},
		{"in 1 hour", "in 1 hour", 1 * time.Hour},
		{"in 30 sec", "in 30 sec", 30 * time.Second},
		{"in 2 hrs", "in 2 hrs", 2 * time.Hour},
		{"in half an hour", "in half an hour", 30 * time.Minute},
		{"in 5 m", "in 5 m", 5 * time.Minute},
		{"in 3 h", "in 3 h", 3 * time.Hour},
		{"tomorrow morning", "tomorrow morning", tomorrowMorning.Sub(base)},
		// amount is now optional and independent of the a/an form:
		{"in hour", "in hour", 1 * time.Hour},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTime(tc.input, base, time.UTC)
			if err != nil {
				t.Fatalf("ParseTime(%q) returned unexpected error: %v", tc.input, err)
			}
			if d := got.Sub(base); d != tc.expected {
				t.Errorf("ParseTime(%q) = %v (delta %v), want delta %v", tc.input, got, d, tc.expected)
			}
		})
	}
}
