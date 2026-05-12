package agentcore

import (
	"testing"

	"github.com/famclaw/famclaw/internal/compress"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestLLMToCompressMarksPrunableByIndex(t *testing.T) {
	in := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "tool", Content: "result"},
	}
	c := llmToCompress(in, map[int]bool{2: true})
	if c[0].Prunable {
		t.Error("idx 0 should not be Prunable")
	}
	if c[1].Prunable {
		t.Error("idx 1 should not be Prunable")
	}
	if !c[2].Prunable {
		t.Error("idx 2 should be Prunable")
	}
	if c[0].Pinned || c[1].Pinned || c[2].Pinned {
		t.Error("Pinned should not be set by bridge — Compress handles system protection")
	}
}

func TestLLMToCompressEmpty(t *testing.T) {
	c := llmToCompress(nil, nil)
	if len(c) != 0 {
		t.Errorf("expected empty, got %d", len(c))
	}
}

func TestCompressToLLMFullKeepPreservesToolCalls(t *testing.T) {
	orig := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "calling tool", ToolCalls: []llm.ToolCall{
			{ID: "call1", Function: llm.ToolCallFunction{Name: "x"}},
		}},
		{Role: "tool", Content: "result", ToolCallID: "call1"},
	}
	c := []compress.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "calling tool"},
		{Role: "tool", Content: "result"},
	}
	out := compressToLLM(c, orig)
	if len(out) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(out))
	}
	if len(out[2].ToolCalls) != 1 || out[2].ToolCalls[0].ID != "call1" {
		t.Error("assistant ToolCalls not preserved")
	}
	if out[3].ToolCallID != "call1" {
		t.Error("tool ToolCallID not preserved")
	}
}

func TestCompressToLLMDropsWalkPastSkipped(t *testing.T) {
	orig := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "a"},
		{Role: "tool", Content: "big result"}, // would be dropped
		{Role: "user", Content: "b"},
	}
	// Simulate compress dropping the tool message
	c := []compress.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "a"},
		{Role: "user", Content: "b"},
	}
	out := compressToLLM(c, orig)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages after drop, got %d", len(out))
	}
	if out[2].Content != "b" {
		t.Errorf("expected 'b' at idx 2, got %q", out[2].Content)
	}
}

func TestCompressToLLMHandlesSyntheticInsertion(t *testing.T) {
	// Simulates the Tier 1 summary case where compress inserts a message
	// that's not in the original.
	orig := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
	}
	c := []compress.Message{
		{Role: "system", Content: "sys"},
		{Role: "system", Content: "[summary]: prior turns covered X"}, // synthetic
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
	}
	out := compressToLLM(c, orig)
	if len(out) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(out))
	}
	if out[1].Content != "[summary]: prior turns covered X" {
		t.Errorf("expected synthetic summary at idx 1, got %q", out[1].Content)
	}
	if len(out[1].ToolCalls) != 0 {
		t.Error("synthetic message should have no ToolCalls")
	}
}

func TestCompressToLLMDuplicateContentSafe(t *testing.T) {
	// Two assistant messages with same content — neither should
	// short-circuit to the first match. The lockstep walker keeps both.
	orig := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "assistant", Content: "ok", ToolCallID: "first"},
		{Role: "assistant", Content: "ok", ToolCallID: "second"},
	}
	c := []compress.Message{
		{Role: "system", Content: "sys"},
		{Role: "assistant", Content: "ok"},
		{Role: "assistant", Content: "ok"},
	}
	out := compressToLLM(c, orig)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(out))
	}
	if out[1].ToolCallID != "first" || out[2].ToolCallID != "second" {
		t.Errorf("duplicate-content order not preserved: %q, %q",
			out[1].ToolCallID, out[2].ToolCallID)
	}
}
