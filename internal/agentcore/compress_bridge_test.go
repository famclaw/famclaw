package agentcore

import (
	"strings"
	"testing"

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
	// Full keep: all positions retained.
	idx := []int{0, 1, 2, 3}
	out := compressToLLM(orig, idx)
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

func TestCompressToLLMDropsByPosition(t *testing.T) {
	orig := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "a"},
		{Role: "tool", Content: "big result"}, // would be dropped
		{Role: "user", Content: "b"},
	}
	// Drop position 2 (the tool result).
	idx := []int{0, 1, 3}
	out := compressToLLM(orig, idx)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages after drop, got %d", len(out))
	}
	if out[2].Content != "b" {
		t.Errorf("expected 'b' at idx 2, got %q", out[2].Content)
	}
}

func TestCompressToLLMDuplicateContentSafe(t *testing.T) {
	// Two user messages with identical text but different positions.
	// Position-based restoration picks the exact right one — no aliasing.
	orig := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "same text"},
		{Role: "user", Content: "same text"},
		{Role: "user", Content: "same text"},
		{Role: "user", Content: "same text"},
	}
	// Select positions 0, 1, 4 (drop 2 and 3).
	idx := []int{0, 1, 4}
	out := compressToLLM(orig, idx)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(out))
	}
	// All three should be "same text" (positions 1 and 4), plus system.
	if out[0].Content != "sys" {
		t.Errorf("expected 'sys', got %q", out[0].Content)
	}
	if out[1].Content != "same text" || out[2].Content != "same text" {
		t.Errorf("expected two 'same text', got %q, %q", out[1].Content, out[2].Content)
	}
}

func TestCompressToLLMEqualTextDifferentImages(t *testing.T) {
	// Equal-text user messages with different ContentParts (images).
	// Position-based restoration must pick the exact original including
	// its specific ContentParts — no Role+Content matching can
	// distinguish these.
	orig := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "look", ContentParts: []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "img1"}},
		}},
		{Role: "user", Content: "look", ContentParts: []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "img2"}},
		}},
		{Role: "user", Content: "look", ContentParts: []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "img3"}},
		}},
		{Role: "user", Content: "look", ContentParts: []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "img4"}},
		}},
	}
	// Select positions 0 and 3 (the img3 user message at index 3).
	// orig[0]=sys, orig[1]=img1, orig[2]=img2, orig[3]=img3, orig[4]=img4
	idx := []int{0, 3}
	out := compressToLLM(orig, idx)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	// Verify the exact ContentParts from position 2.
	cparts, ok := out[1].ContentParts[0].(map[string]any)
	if !ok {
		t.Fatalf("ContentParts[0] not a map")
	}
	urlMap, _ := cparts["image_url"].(map[string]any)
	url, _ := urlMap["url"].(string)
	if url != "img3" {
		t.Errorf("expected img3 at position 2, got %q", url)
	}
}

// TestCompressToLLMRestoresRenamedToolCall: the tool name in the
// assistant message is part of the original llm.ToolCall and must survive
// the bridge even when the tool was renamed by the model.
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
	// Full keep.
	idx := []int{0, 1, 2, 3, 4, 5, 6, 7}
	out := compressToLLM(orig, idx)
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

// TestLLMToCompressToolArgsSameNameDifferentArgs verifies that parallel
// calls to the same tool name each preserve their own serialized argument
// payload in ToolArgs (one entry per call, in CallIDs order). No keying
// by name — no overwrite. The budget behavior is verified through the
// live bridge: a large argument in an earlier call must be counted.
func TestLLMToCompressToolArgsSameNameDifferentArgs(t *testing.T) {
	bigArg := make(map[string]any)
	bigArg["url"] = strings.Repeat("a", 4000) // 4000-char URL
	smallArg := map[string]any{"url": "https://short.example"}

	in := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "fetch these"},
		{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{
			{ID: "call_1", Function: llm.ToolCallFunction{Name: "fetch", Arguments: bigArg}},
			{ID: "call_2", Function: llm.ToolCallFunction{Name: "fetch", Arguments: smallArg}},
		}},
		{Role: "tool", Content: "result_1", ToolCallID: "call_1"},
		{Role: "tool", Content: "result_2", ToolCallID: "call_2"},
	}
	c := llmToCompress(in, map[int]bool{3: true, 4: true})

	// ToolArgs must have exactly 2 entries (one per call), not 1.
	if len(c[2].ToolArgs) != 2 {
		t.Fatalf("expected 2 ToolArgs entries, got %d", len(c[2].ToolArgs))
	}
	// The first entry (big arg) must be ~4000 chars.
	if len(c[2].ToolArgs[0]) < 3000 {
		t.Errorf("ToolArgs[0] too short: %d chars (big arg should be ~4000)", len(c[2].ToolArgs[0]))
	}
	// The second entry (small arg) must be short.
	if len(c[2].ToolArgs[1]) > 200 {
		t.Errorf("ToolArgs[1] too long: %d chars (small arg should be <200)", len(c[2].ToolArgs[1]))
	}
	// Both entries are distinct — no overwrite.
	if c[2].ToolArgs[0] == c[2].ToolArgs[1] {
		t.Error("ToolArgs[0] == ToolArgs[1] — earlier payload was overwritten")
	}
}
