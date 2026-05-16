package agentcore

import (
	"strings"
	"testing"
)

// TestActionLoopDetector_Nudge is a table-driven suite over the detector's
// repetition-detection behavior: each case pushes a sequence of actions onto
// a fresh detector and asserts on the resulting Nudge() output.
func TestActionLoopDetector_Nudge(t *testing.T) {
	cases := []struct {
		name        string
		push        func(d *ActionLoopDetector)
		wantInNudge string // "" means expect an empty nudge
	}{
		{
			name: "no repetition",
			push: func(d *ActionLoopDetector) {
				for i, name := range []string{"navigate", "click", "fill", "extract", "snapshot"} {
					d.Push(name, map[string]any{"i": i})
				}
			},
			wantInNudge: "",
		},
		{
			name: "five same triggers 5-threshold",
			push: func(d *ActionLoopDetector) {
				for range 5 {
					d.Push("builtin__browser_click", map[string]any{"ref": "e1"})
				}
			},
			wantInNudge: "If this repetition is intentional",
		},
		{
			name: "eight same triggers 8-threshold",
			push: func(d *ActionLoopDetector) {
				for range 8 {
					d.Push("builtin__browser_click", map[string]any{"ref": "e1"})
				}
			},
			wantInNudge: "Reconsider — call browser_extract",
		},
		{
			name: "twelve same triggers STOP",
			push: func(d *ActionLoopDetector) {
				for range 12 {
					d.Push("builtin__browser_click", map[string]any{"ref": "e1"})
				}
			},
			wantInNudge: "STOP",
		},
		{
			name: "alternating unique actions — no nudge",
			push: func(d *ActionLoopDetector) {
				// Each push has a distinct args map ({"i": i}) so every hash
				// is unique — no action accumulates a repetition count.
				for i := range 30 {
					name := "action_a"
					if i%2 == 1 {
						name = "action_b"
					}
					d.Push(name, map[string]any{"i": i})
				}
			},
			wantInNudge: "",
		},
		{
			name: "key-order-normalized args hash identically",
			push: func(d *ActionLoopDetector) {
				// {a,b} and {b,a} must produce the same hash — 10 identical
				// hashes total, enough for the 8-threshold.
				for range 5 {
					d.Push("builtin__browser_fill", map[string]any{"a": 1, "b": 2})
				}
				for range 5 {
					d.Push("builtin__browser_fill", map[string]any{"b": 2, "a": 1})
				}
			},
			wantInNudge: "Reconsider — call browser_extract",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewActionLoopDetector(20)
			tc.push(d)
			got := d.Nudge()
			if tc.wantInNudge == "" {
				if got != "" {
					t.Errorf("expected empty nudge, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantInNudge) {
				t.Errorf("expected nudge containing %q, got %q", tc.wantInNudge, got)
			}
		})
	}
}

// TestActionLoopDetector_DefaultCap verifies that a non-positive cap passed to
// NewActionLoopDetector defaults to 20: the rolling window must evict at 20
// entries and the >=12 STOP threshold must still fire. Kept separate from the
// Nudge table because it asserts on the internal window length.
func TestActionLoopDetector_DefaultCap(t *testing.T) {
	cases := []struct {
		name string
		cap  int
	}{
		{"zero cap defaults to 20", 0},
		{"negative cap defaults to 20", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewActionLoopDetector(tc.cap)
			for range 25 {
				d.Push("act", map[string]any{})
			}
			if !strings.Contains(d.Nudge(), "STOP") {
				t.Errorf("cap=%d: expected STOP nudge after 25 same-action pushes", tc.cap)
			}
			if len(d.window) != 20 {
				t.Errorf("cap=%d: expected window size 20, got %d", tc.cap, len(d.window))
			}
		})
	}
}

// TestActionLoopDetector_ZeroValuePushNoPanic verifies that a zero-value
// detector (constructed without NewActionLoopDetector) does not panic on
// Push — Push applies a defensive cap default.
func TestActionLoopDetector_ZeroValuePushNoPanic(t *testing.T) {
	var d ActionLoopDetector // zero value: cap == 0
	for range 25 {
		d.Push("act", map[string]any{})
	}
	if len(d.window) != 20 {
		t.Errorf("zero-value detector: expected window size 20 after default, got %d", len(d.window))
	}
}
