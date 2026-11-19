package compress

import (
	"strings"
	"testing"
)

// Tests for Indices: the drop-only index map must let a caller restore
// original entries by position, including tool-call identities that are
// not visible in Role/Content.

func TestIndicesDropOnlySubset(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "", CallIDs: []string{"call_a"}, Prunable: true},
		{Role: "tool", Content: strings.Repeat("R", 2000), ToolCallID: "call_a", Prunable: true},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "reply2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "reply3"},
	}
	opts := Options{ContextWindow: 400}
	out := Compress(msgs, opts)
	idx := Indices(msgs, opts)
	if len(out) != len(idx) {
		t.Fatalf("len(out)=%d, len(idx)=%d", len(out), len(idx))
	}
	for i, oi := range idx {
		if oi < 0 || oi >= len(msgs) {
			t.Fatalf("idx[%d]=%d out of range", i, oi)
		}
		if out[i].Role != msgs[oi].Role || out[i].Content != msgs[oi].Content {
			t.Fatalf("idx[%d]=%d: restored message differs from out[%d]: %+v vs %+v", i, oi, i, msgs[oi], out[i])
		}
		if len(out[i].CallIDs) != len(msgs[oi].CallIDs) {
			t.Fatalf("idx[%d]: CallIDs not restored", i)
		}
		if out[i].ToolCallID != msgs[oi].ToolCallID {
			t.Fatalf("idx[%d]: ToolCallID not restored: %q vs %q", i, out[i].ToolCallID, msgs[oi].ToolCallID)
		}
	}
}

func TestIndicesDistinctOriginalsSameContent(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "", CallIDs: []string{"call_x", "call_y"}},
		{Role: "tool", Content: "same", ToolCallID: "call_x"},
		{Role: "tool", Content: "same", ToolCallID: "call_y"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}
	opts := Options{ContextWindow: 40000}
	idx := Indices(msgs, opts)
	if len(idx) != len(msgs) {
		t.Fatalf("expected %d entries, got %d", len(msgs), len(idx))
	}
	for i, oi := range idx {
		if oi != i {
			t.Errorf("idx[%d]=%d, want %d", i, oi, i)
		}
	}
}

func TestIndicesForcedEvictionDuplicateContent(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "", CallIDs: []string{"call_p", "call_q"}, Prunable: true},
		{Role: "tool", Content: "dup", ToolCallID: "call_p", Prunable: true},
		{Role: "tool", Content: "dup", ToolCallID: "call_q", Prunable: true},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}
	opts := Options{ContextWindow: 200}
	out := Compress(msgs, opts)
	idx := Indices(msgs, opts)
	if len(out) != len(idx) {
		t.Fatalf("len(out)=%d, len(idx)=%d", len(out), len(idx))
	}
	for i, oi := range idx {
		if oi < 0 || oi >= len(msgs) {
			t.Fatalf("idx[%d]=%d out of range", i, oi)
		}
		if out[i].Role != msgs[oi].Role || out[i].Content != msgs[oi].Content {
			t.Fatalf("idx[%d]=%d: role/content mismatch: %+v vs %+v", i, oi, msgs[oi], out[i])
		}
	}
	var dupIdx []int
	for _, oi := range idx {
		if msgs[oi].Content == "dup" {
			dupIdx = append(dupIdx, oi)
		}
	}
	if len(dupIdx) == 2 && dupIdx[0] > dupIdx[1] {
		t.Errorf("duplicate-content indices out of order: %v", dupIdx)
	}
}
