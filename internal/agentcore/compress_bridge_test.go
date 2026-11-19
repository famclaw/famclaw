package agentcore

import (
	"testing"

	"github.com/famclaw/famclaw/internal/compress"
	"github.com/famclaw/famclaw/internal/llm"
)

// cmsgsOf builds a []compress.Message mirror of in with no Prunable/Pinned
// flags, for bridge tests that only exercise the role/content/tool-identity
// mapping.
func cmsgsOf(in []llm.Message) []compress.Message {
	out := make([]compress.Message, len(in))
	for i, m := range in {
		out[i] = llmToCompress([]llm.Message{m}, nil)[0]
	}
	return out
}

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

func TestLLMToCompressCarriesToolIdentities(t *testing.T) {
	in := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "calling", ToolCalls: []llm.ToolCall{
			{ID: "call_a", Function: llm.ToolCallFunction{Name: "tool_x"}},
			{ID: "call_b", Function: llm.ToolCallFunction{Name: "tool_y"}},
		}},
		{Role: "tool", Content: "result_a", ToolCallID: "call_a"},
		{Role: "tool", Content: "result_b", ToolCallID: "call_b"},
	}
	c := llmToCompress(in, map[int]bool{3: true, 4: true})
	if len(c[2].CallIDs) != 2 || c[2].CallIDs[0] != "call_a" || c[2].CallIDs[1] != "call_b" {
		t.Errorf("assistant CallIDs not carried: %+v", c[2].CallIDs)
	}
	if c[3].ToolCallID != "call_a" {
		t.Errorf("tool[0] ToolCallID = %q, want call_a", c[3].ToolCallID)
	}
	if c[4].ToolCallID != "call_b" {
		t.Errorf("tool[1] ToolCallID = %q, want call_b", c[4].ToolCallID)
	}
	if !c[3].Prunable || !c[4].Prunable {
		t.Error("tool results should be Prunable")
	}
	if c[0].ToolCallID != "" || len(c[0].CallIDs) > 0 {
		t.Error("system message should have no tool identity")
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
	cmsgs := cmsgsOf(orig)
	compressed := cmsgs // full keep
	out := compressToLLM(cmsgs, compressed, orig)
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
	cmsgs := cmsgsOf(orig)
	// Simulate compress dropping the tool message
	compressed := []compress.Message{cmsgs[0], cmsgs[1], cmsgs[3]}
	out := compressToLLM(cmsgs, compressed, orig)
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
	cmsgs := cmsgsOf(orig)
	compressed := []compress.Message{
		cmsgs[0],
		{Role: "system", Content: "[summary]: prior turns covered X"}, // synthetic
		cmsgs[1],
		cmsgs[2],
	}
	out := compressToLLM(cmsgs, compressed, orig)
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
	cmsgs := cmsgsOf(orig)
	compressed := cmsgs
	out := compressToLLM(cmsgs, compressed, orig)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(out))
	}
	if out[1].ToolCallID != "first" || out[2].ToolCallID != "second" {
		t.Errorf("duplicate-content order not preserved: %q, %q",
			out[1].ToolCallID, out[2].ToolCallID)
	}
}

// TestCompressToLLMRestoresRenamedToolCall: the tool name in the
// assistant message is part of the original llm.ToolCall and must survive
// the bridge even when the tool was renamed by the model (the schema and
// result are unchanged — only the function name differs between the
// request and any later representation).
func TestCompressToLLMRestoresRenamedToolCall(t *testing.T) {
	orig := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{
			{ID: "call_rn", Function: llm.ToolCallFunction{Name: "builtin__web_search", Arguments: map[string]any{"query": "x"}}},
		}},
		{Role: "tool", Content: "search results", ToolCallID: "call_rn"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}
	cmsgs := cmsgsOf(orig)
	compressed := cmsgs // full keep
	out := compressToLLM(cmsgs, compressed, orig)
	if len(out) != 8 {
		t.Fatalf("expected 8 messages, got %d", len(out))
	}
	if len(out[2].ToolCalls) != 1 || out[2].ToolCalls[0].ID != "call_rn" {
		t.Fatalf("renamed tool call not preserved: %+v", out[2].ToolCalls)
	}
	if out[2].ToolCalls[0].Function.Name != "builtin__web_search" {
		t.Errorf("renamed tool name = %q, want builtin__web_search", out[2].ToolCalls[0].Function.Name)
	}
	if out[3].ToolCallID != "call_rn" {
		t.Errorf("tool result ToolCallID = %q, want call_rn", out[3].ToolCallID)
	}
}
