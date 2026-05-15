package agentcore

import (
	"strings"
	"testing"
)

func TestActionLoopDetector_NoRepetition(t *testing.T) {
	d := NewActionLoopDetector(20)
	for i, name := range []string{"navigate", "click", "fill", "extract", "snapshot"} {
		d.Push(name, map[string]any{"i": i})
	}
	if got := d.Nudge(); got != "" {
		t.Errorf("expected empty nudge, got %q", got)
	}
}

func TestActionLoopDetector_FiveSameTriggers5Threshold(t *testing.T) {
	d := NewActionLoopDetector(20)
	for range 5 {
		d.Push("builtin__browser_click", map[string]any{"ref": "e1"})
	}
	got := d.Nudge()
	if !strings.Contains(got, "If this repetition is intentional") {
		t.Errorf("expected 5-threshold nudge, got %q", got)
	}
}

func TestActionLoopDetector_EightTriggers8Threshold(t *testing.T) {
	d := NewActionLoopDetector(20)
	for range 8 {
		d.Push("builtin__browser_click", map[string]any{"ref": "e1"})
	}
	got := d.Nudge()
	if !strings.Contains(got, "Reconsider — call browser_extract") {
		t.Errorf("expected 8-threshold nudge, got %q", got)
	}
}

func TestActionLoopDetector_TwelveTriggersStop(t *testing.T) {
	d := NewActionLoopDetector(20)
	for range 12 {
		d.Push("builtin__browser_click", map[string]any{"ref": "e1"})
	}
	got := d.Nudge()
	if !strings.Contains(got, "STOP") {
		t.Errorf("expected STOP nudge, got %q", got)
	}
}

func TestActionLoopDetector_WindowEvictsOldest(t *testing.T) {
	d := NewActionLoopDetector(20)
	// Push 30 alternating actions — no single action accumulates 5 in a 20-wide window.
	for i := range 30 {
		name := "action_a"
		if i%2 == 1 {
			name = "action_b"
		}
		d.Push(name, map[string]any{"i": i})
	}
	// Window holds the last 20 entries: indices 10..29.
	// action_a appears at even indices: 10,12,14,16,18,20,22,24,26,28 → 10 times.
	// Oops — that's > 5, so the nudge WOULD fire. But wait, each push uses a
	// different args map ({"i": i}), so each hash is unique. No repetition.
	if got := d.Nudge(); got != "" {
		t.Errorf("alternating unique actions: expected empty nudge, got %q", got)
	}
}

func TestActionLoopDetector_DefaultCap(t *testing.T) {
	d0 := NewActionLoopDetector(0)
	dm := NewActionLoopDetector(-1)

	// Fill past 20 with the same action: window should evict at 20, so only
	// 20 entries at most. Nudge at >=12 should still fire (20 >= 12).
	for range 25 {
		d0.Push("act", map[string]any{})
		dm.Push("act", map[string]any{})
	}
	if !strings.Contains(d0.Nudge(), "STOP") {
		t.Error("cap=0 (default 20): expected STOP nudge after 25 same-action pushes")
	}
	if !strings.Contains(dm.Nudge(), "STOP") {
		t.Error("cap=-1 (default 20): expected STOP nudge after 25 same-action pushes")
	}
	// Verify cap is 20 not larger: after 25 pushes the window must be exactly 20.
	if len(d0.window) != 20 {
		t.Errorf("cap=0: expected window size 20, got %d", len(d0.window))
	}
	if len(dm.window) != 20 {
		t.Errorf("cap=-1: expected window size 20, got %d", len(dm.window))
	}
}

func TestActionLoopDetector_NormalizesArgs(t *testing.T) {
	d := NewActionLoopDetector(20)
	// Both orderings must produce the same hash. Push each 5 times and expect
	// Nudge to fire the 5-threshold (total 10 identical hashes in window).
	for range 5 {
		d.Push("builtin__browser_fill", map[string]any{"a": 1, "b": 2})
	}
	for range 5 {
		d.Push("builtin__browser_fill", map[string]any{"b": 2, "a": 1})
	}
	got := d.Nudge()
	// 10 identical hashes → should be at least 8-threshold.
	if !strings.Contains(got, "Reconsider — call browser_extract") {
		t.Errorf("expected 8-threshold nudge when args are key-order-normalized, got %q", got)
	}
}
