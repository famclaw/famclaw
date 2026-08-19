package reminder

import (
	"testing"
	"time"
)

func TestParseTime(t *testing.T) {
	base := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		input    string
		expected time.Time
		wantErr  bool
	}{
		// Relative times
		{"in 5 minutes", "in 5 minutes", base.Add(5 * time.Minute), false},
		{"in 1 minute", "in 1 minute", base.Add(1 * time.Minute), false},
		{"in a minute", "in a minute", base.Add(1 * time.Minute), false},
		{"in an hour", "in an hour", base.Add(1 * time.Hour), false},
		{"in 2 hours", "in 2 hours", base.Add(2 * time.Hour), false},
		{"in 1 day", "in 1 day", base.Add(24 * time.Hour), false},
		{"in 3 days", "in 3 days", base.Add(72 * time.Hour), false},

		// Absolute times (today)
		{"at 14:30", "at 14:30", time.Date(2026, 1, 15, 14, 30, 0, 0, time.UTC), false},
		{"at 9:05 am", "at 9:05 am", time.Date(2026, 1, 16, 9, 5, 0, 0, time.UTC), false},
		{"at 9:05 pm", "at 9:05 pm", time.Date(2026, 1, 15, 21, 5, 0, 0, time.UTC), false},
		{"at 12:00 am", "at 12:00 am", time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC), false},
		{"at 12:00 pm", "at 12:00 pm", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC), false},

		// Tomorrow
		{"tomorrow at 9:00", "tomorrow at 9:00", time.Date(2026, 1, 16, 9, 0, 0, 0, time.UTC), false},
		{"tomorrow 10:30 am", "tomorrow 10:30 am", time.Date(2026, 1, 16, 10, 30, 0, 0, time.UTC), false},

		// Today
		{"today at 15:00", "today at 15:00", time.Date(2026, 1, 15, 15, 0, 0, 0, time.UTC), false},

		// Day of week
		{"monday at 10:00", "monday at 10:00", time.Date(2026, 1, 19, 10, 0, 0, 0, time.UTC), false},
		{"next friday at 15:00", "next friday at 15:00", time.Date(2026, 1, 23, 15, 0, 0, 0, time.UTC), false},

		// Shorthand
		{"5m", "5m", base.Add(5 * time.Minute), false},
		{"2h", "2h", base.Add(2 * time.Hour), false},
		{"1d", "1d", base.Add(24 * time.Hour), false},
		{"30s", "30s", base.Add(30 * time.Second), false},

		// Edge cases - time already passed today -> next day
		{"at 10:00 (passed)", "at 10:00", time.Date(2026, 1, 16, 10, 0, 0, 0, time.UTC), false},

		// Errors
		{"empty", "", time.Time{}, true},
		{"invalid", "nonsense", time.Time{}, true},
		{"invalid hour", "at 25:00", time.Time{}, true},
		{"invalid minute", "at 12:60", time.Time{}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTime(tc.input, base, time.UTC)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if !got.Equal(tc.expected) {
				t.Errorf("got %v, want %v", got, tc.expected)
			}
		})
	}
}

// TestParseTimeDayOffsetWithTime covers issue #366: a day offset combined
// with an explicit time of day must be honored exactly ("in 2 days at
// 5:00 pm" -> two calendar days out at 17:00), while the ambiguous phrasings
// (bare "in N days") keep their old behavior (current time N days from now).
func TestParseTimeDayOffsetWithTime(t *testing.T) {
	base := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) // Thursday

	tests := []struct {
		name     string
		input    string
		expected time.Time
		wantErr  bool
	}{
		// Explicit time, days out (the #366 case)
		{"2 days out at 17:00", "in 2 days at 17:00", time.Date(2026, 1, 17, 17, 0, 0, 0, time.UTC), false},
		{"2 days out without 'at'", "in 2 days 17:00", time.Date(2026, 1, 17, 17, 0, 0, 0, time.UTC), false},
		{"2 days out 5:00 pm", "in 2 days at 5:00 pm", time.Date(2026, 1, 17, 17, 0, 0, 0, time.UTC), false},
		{"2 days out bare '5 pm'", "in 2 days at 5 pm", time.Date(2026, 1, 17, 17, 0, 0, 0, time.UTC), false},
		{"1 day out 8:30 am", "in 1 day at 8:30 am", time.Date(2026, 1, 16, 8, 30, 0, 0, time.UTC), false},
		{"3 days out 06:15", "in 3 days at 06:15", time.Date(2026, 1, 18, 6, 15, 0, 0, time.UTC), false},
		{"12:00 am midnight", "in 2 days at 12:00 am", time.Date(2026, 1, 17, 0, 0, 0, 0, time.UTC), false},
		{"12:00 pm noon", "in 2 days at 12:00 pm", time.Date(2026, 1, 17, 12, 0, 0, 0, time.UTC), false},
		// Zero-day offset behaves like today's "at HH:MM"
		{"0 days future time", "in 0 days at 17:00", time.Date(2026, 1, 15, 17, 0, 0, 0, time.UTC), false},
		{"0 days passed time rolls to tomorrow", "in 0 days at 9:00", time.Date(2026, 1, 16, 9, 0, 0, 0, time.UTC), false},

		// Meridiem-aware hour validation (reviewer suggestion on #367):
		// with am/pm the hour must be a 12-hour value (1-12), without it a
		// 24-hour value (0-23).
		{"12:xx pm still noon", "in 2 days at 12:30 pm", time.Date(2026, 1, 17, 12, 30, 0, 0, time.UTC), false},
		{"1:xx am still early morning", "in 1 day at 1:30 am", time.Date(2026, 1, 16, 1, 30, 0, 0, time.UTC), false},
		{"24h form without meridiem still parses", "in 2 days at 13:00", time.Date(2026, 1, 17, 13, 0, 0, 0, time.UTC), false},
		{"24h midnight without meridiem still parses", "in 2 days at 0:00", time.Date(2026, 1, 17, 0, 0, 0, 0, time.UTC), false},
		{"hour 13 with meridiem rejected", "in 2 days at 13:00 pm", time.Time{}, true},
		{"hour 14 with meridiem rejected", "in 2 days at 14:00 am", time.Time{}, true},
		{"hour 0 with meridiem rejected", "in 2 days at 0:00 pm", time.Time{}, true},

		// Today with explicit time keeps working (unchanged)
		{"today at 17:00", "at 17:00", time.Date(2026, 1, 15, 17, 0, 0, 0, time.UTC), false},
		{"today at 15:00 keyword", "today at 15:00", time.Date(2026, 1, 15, 15, 0, 0, 0, time.UTC), false},

		// Ambiguous phrasings keep their old behavior
		{"bare in 2 days keeps current time", "in 2 days", base.Add(48 * time.Hour), false},
		{"bare in 3 days keeps current time", "in 3 days", base.Add(72 * time.Hour), false},
		{"in 2 hours unchanged", "in 2 hours", base.Add(2 * time.Hour), false},

		// Errors
		{"hour 25 rejected", "in 2 days at 25:00", time.Time{}, true},
		{"minute 60 rejected", "in 2 days at 12:60", time.Time{}, true},
		{"bare hour without meridiem rejected", "in 2 days at 5", time.Time{}, true},
		{"trailing junk rejected", "in 2 days at 17:00 sharp", time.Time{}, true},
		{"missing day count rejected", "in days at 17:00", time.Time{}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTime(tc.input, base, time.UTC)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if !got.Equal(tc.expected) {
				t.Errorf("got %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestParseDayOfWeek(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Weekday
	}{
		{"monday", time.Monday},
		{"tuesday", time.Tuesday},
		{"wednesday", time.Wednesday},
		{"thursday", time.Thursday},
		{"friday", time.Friday},
		{"saturday", time.Saturday},
		{"sunday", time.Sunday},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := parseDayOfWeek(tc.input)
			if got != tc.expected {
				t.Errorf("got %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		input    time.Duration
		expected string
	}{
		{30 * time.Second, "less than a minute"},
		{1 * time.Minute, "1 minute"},
		{5 * time.Minute, "5 minutes"},
		{1 * time.Hour, "1 hour"},
		{3 * time.Hour, "3 hours"},
		{24 * time.Hour, "1 day"},
		{48 * time.Hour, "2 days"},
	}

	for _, tc := range tests {
		t.Run(tc.expected, func(t *testing.T) {
			got := FormatDuration(tc.input)
			if got != tc.expected {
				t.Errorf("got %q, want %q", got, tc.expected)
			}
		})
	}
}
