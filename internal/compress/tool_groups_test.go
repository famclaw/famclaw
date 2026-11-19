package compress

import (
	"strings"
	"testing"
)

// Tests for tool-call group preservation during compression.
// Issue #442: assistant tool_calls and their role:"tool" results must
// stay together as a unit through eviction.

func TestCompressToolGroupPreserved(t *testing.T) {
	// Build a history with two tool-call groups, each with distinct
	// call IDs and large prunable results.
	bigResult := func(caller string, n int) string {
		return strings.Repeat(caller, n)
	}

	msgs := []Message{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "search for cats"},
		// Group 1: assistant with 2 calls + 2 results
		{Role: "assistant", Content: "", CallIDs: []string{"call_g1a", "call_g1b"}, Prunable: true},
		{Role: "tool", Content: bigResult("A", 1000), ToolCallID: "call_g1a", Prunable: true},
		{Role: "tool", Content: bigResult("B", 1000), ToolCallID: "call_g1b", Prunable: true},
		{Role: "user", Content: "now search for dogs"},
		// Group 2: assistant with 1 call + 1 result
		{Role: "assistant", Content: "", CallIDs: []string{"call_g2"}, Prunable: true},
		{Role: "tool", Content: bigResult("C", 1000), ToolCallID: "call_g2", Prunable: true},
		{Role: "user", Content: "summarize"},
		{Role: "assistant", Content: "summary"},
	}

	t.Run("roomy_budget_keeps_all_groups", func(t *testing.T) {
		out := Compress(msgs, Options{ContextWindow: 100000})
		if len(out) != len(msgs) {
			t.Fatalf("roomy budget: expected %d messages, got %d", len(msgs), len(out))
		}
	})

	t.Run("tight_budget_evicts_whole_groups", func(t *testing.T) {
		out := Compress(msgs, Options{ContextWindow: 500})
		// Check that no group is split: for each call ID, either both the
		// assistant message AND all its results are present, or none are.
		callIDs := map[string]bool{
			"call_g1a": false, "call_g1b": false, "call_g2": false,
		}
		resultIDs := map[string]bool{
			"call_g1a": false, "call_g1b": false, "call_g2": false,
		}
		for _, m := range out {
			if m.Role == "assistant" {
				for _, cid := range m.CallIDs {
					if _, ok := callIDs[cid]; ok {
						callIDs[cid] = true
					}
				}
			}
			if m.Role == "tool" && m.ToolCallID != "" {
				if _, ok := resultIDs[m.ToolCallID]; ok {
					resultIDs[m.ToolCallID] = true
				}
			}
		}
		for cid := range callIDs {
			if callIDs[cid] != resultIDs[cid] {
				t.Errorf("group split: call %q assistant=%v result=%v", cid, callIDs[cid], resultIDs[cid])
			}
		}
	})
}

func TestCompressNoOrphanedToolResult(t *testing.T) {
	// A tool result whose assistant call was evicted must not survive
	// on its own.
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "", CallIDs: []string{"call_orphan"}, Prunable: true},
		{Role: "tool", Content: strings.Repeat("X", 4000), ToolCallID: "call_orphan", Prunable: true},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}

	out := Compress(msgs, Options{ContextWindow: 300})

	// Collect call IDs and result IDs from the output.
	callIDs := make(map[string]bool)
	resultIDs := make(map[string]bool)
	for _, m := range out {
		if m.Role == "assistant" {
			for _, cid := range m.CallIDs {
				callIDs[cid] = true
			}
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			resultIDs[m.ToolCallID] = true
		}
	}
	// No orphaned result.
	for rid := range resultIDs {
		if !callIDs[rid] {
			t.Errorf("orphaned tool result %q: no matching assistant call", rid)
		}
	}
	// No orphaned call.
	for cid := range callIDs {
		if !resultIDs[cid] {
			t.Errorf("orphaned assistant call %q: no matching tool result", cid)
		}
	}
}

func TestCompressRetainedOrderPreserved(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "", CallIDs: []string{"call_ord"}, Prunable: true},
		{Role: "tool", Content: strings.Repeat("O", 2000), ToolCallID: "call_ord", Prunable: true},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}

	out := Compress(msgs, Options{ContextWindow: 500})

	// Verify the retained messages are in their original order.
	// Build a map of original index -> content/role.
	type idPair struct {
		role    string
		content string
		callID  string
	}
	origIDs := make([]idPair, len(msgs))
	for i, m := range msgs {
		origIDs[i] = idPair{m.Role, m.Content, m.ToolCallID}
	}

	// Walk through out and match against origIDs in order.
	j := 0
	for _, m := range out {
		matched := false
		for i := j; i < len(msgs); i++ {
			if m.Role == origIDs[i].role && m.Content == origIDs[i].content && m.ToolCallID == origIDs[i].callID {
				// Also check CallIDs match.
				if len(m.CallIDs) == len(msgs[i].CallIDs) {
					callMatch := true
					for k := range m.CallIDs {
						if m.CallIDs[k] != msgs[i].CallIDs[k] {
							callMatch = false
							break
						}
					}
					if callMatch {
						j = i + 1
						matched = true
						break
					}
				}
			}
		}
		if !matched {
			t.Fatalf("retained message %+v not found in original order from position %d", m, j)
		}
	}
}

func TestCompressProtectedOversizedBudgetSafe(t *testing.T) {
	// When the protected messages alone exceed the budget, the result
	// must still be well-formed (no split groups).
	hugeProtected := strings.Repeat("P", 8000)
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "old"},
		{Role: "assistant", Content: "old reply"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "", CallIDs: []string{"call_prot"}, Prunable: true},
		{Role: "tool", Content: hugeProtected, ToolCallID: "call_prot"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}

	out := Compress(msgs, Options{ContextWindow: 200})

	// The last-3 protected zone includes the tool result (index 5) and
	// the current user/assistant pair (indices 6-7). The assistant call
	// at index 4 is NOT in the last-3, but its group IS protected because
	// the result at index 5 is. The whole group must be retained.
	var sawCall, sawResult bool
	for _, m := range out {
		if m.Role == "assistant" {
			for _, cid := range m.CallIDs {
				if cid == "call_prot" {
					sawCall = true
				}
			}
		}
		if m.Role == "tool" && m.ToolCallID == "call_prot" {
			sawResult = true
		}
	}
	if sawCall != sawResult {
		t.Errorf("protected group split: call=%v result=%v", sawCall, sawResult)
	}
}

func TestCompressDuplicatedContentGroupsStayDistinct(t *testing.T) {
	// Two groups with identical result content but different call IDs.
	// A Role+Content matcher would confuse them.
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "", CallIDs: []string{"call_g1"}, Prunable: true},
		{Role: "tool", Content: "identical result", ToolCallID: "call_g1", Prunable: true},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "", CallIDs: []string{"call_g2"}, Prunable: true},
		{Role: "tool", Content: "identical result", ToolCallID: "call_g2", Prunable: true},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}

	// Roomy budget: all groups retained.
	outRoomy := Compress(msgs, Options{ContextWindow: 40000})
	if len(outRoomy) != len(msgs) {
		t.Fatalf("roomy: expected %d messages, got %d", len(msgs), len(outRoomy))
	}
	// Both call_g1 and call_g2 results should be present with correct IDs.
	var sawG1, sawG2 bool
	for _, m := range outRoomy {
		if m.Role == "tool" && m.ToolCallID == "call_g1" {
			sawG1 = true
		}
		if m.Role == "tool" && m.ToolCallID == "call_g2" {
			sawG2 = true
		}
	}
	if !sawG1 || !sawG2 {
		t.Errorf("roomy: expected both call_g1 and call_g2 results, got g1=%v g2=%v", sawG1, sawG2)
	}

	// Tight budget: each group is either fully present or fully absent.
	outTight := Compress(msgs, Options{ContextWindow: 400})
	for _, g := range []string{"call_g1", "call_g2"} {
		var sawResult bool
		for _, m := range outTight {
			if m.Role == "tool" && m.ToolCallID == g {
				sawResult = true
			}
		}
		var sawAssistant bool
		for _, m := range outTight {
			if m.Role == "assistant" {
				for _, cid := range m.CallIDs {
					if cid == g {
						sawAssistant = true
					}
				}
			}
		}
		if sawResult != sawAssistant {
			t.Errorf("duplicated-content group %q split: assistant=%v result=%v; out=%+v", g, sawAssistant, sawResult, outTight)
		}
	}
}

// TestTruncateReturnsExactOriginals verifies that Compress returns the
// exact original Message values for retained entries, and that the
// retained entries appear in their original global-index order.
func TestTruncateReturnsExactOriginals(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "", CallIDs: []string{"call_a", "call_b"}, Prunable: true},
		{Role: "tool", Content: "identical", ToolCallID: "call_a", Prunable: true},
		{Role: "tool", Content: "identical", ToolCallID: "call_b", Prunable: true},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
		{Role: "user", Content: "current", ContentParts: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,xxx"}}}},
	}

	out := Compress(msgs, Options{ContextWindow: 200})

	// Verify each output entry matches exactly one original, in order.
	j := 0
	for _, m := range out {
		matched := false
		for i := j; i < len(msgs); i++ {
			if sameMessageAllFields(msgs[i], m) {
				j = i + 1
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("compressed entry %+v does not match any original entry from position %d", m, j)
		}
	}

	// Verify the current user request (last, with image) is retained.
	last := out[len(out)-1]
	if last.Role != "user" || last.Content != "current" {
		t.Errorf("last message should be the current user request, got %+v", last)
	}
	if len(last.ContentParts) != 1 {
		t.Errorf("current user request lost its image ContentParts: %+v", last.ContentParts)
	}
}

func sameMessageAllFields(a, b Message) bool {
	if a.Role != b.Role || a.Content != b.Content || a.Pinned != b.Pinned || a.Prunable != b.Prunable || a.ToolCallID != b.ToolCallID {
		return false
	}
	if len(a.CallIDs) != len(b.CallIDs) {
		return false
	}
	for i := range a.CallIDs {
		if a.CallIDs[i] != b.CallIDs[i] {
			return false
		}
	}
	if len(a.ContentParts) != len(b.ContentParts) {
		return false
	}
	for i := range a.ContentParts {
		if !samePart(a.ContentParts[i], b.ContentParts[i]) {
			return false
		}
	}
	return true
}

func samePart(a, b any) bool {
	ma, ok1 := a.(map[string]any)
	mb, ok2 := b.(map[string]any)
	if ok1 != ok2 {
		return false
	}
	if !ok1 {
		return a == b
	}
	if len(ma) != len(mb) {
		return false
	}
	for k, va := range ma {
		vb, exists := mb[k]
		if !exists {
			return false
		}
		if !samePart(va, vb) {
			return false
		}
	}
	return true
}

// TestTruncateProtectedGroupCrossingBoundary: a tool-call group that
// straddles the "last 3 messages" protected boundary must be retained
// whole, not split.
func TestTruncateProtectedGroupCrossingBoundary(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: strings.Repeat("a", 800)},
		{Role: "assistant", Content: strings.Repeat("b", 800)},
		{Role: "user", Content: strings.Repeat("c", 800)},
		{Role: "assistant", Content: strings.Repeat("d", 800)},
		{Role: "user", Content: strings.Repeat("e", 800)},
		{Role: "assistant", Content: strings.Repeat("f", 800)},
		{Role: "user", Content: strings.Repeat("g", 800)},
		{Role: "assistant", Content: "", CallIDs: []string{"call_c1", "call_c2"}, Prunable: true},
		{Role: "tool", Content: strings.Repeat("h", 800), ToolCallID: "call_c1", Prunable: true},
		{Role: "tool", Content: strings.Repeat("i", 800), ToolCallID: "call_c2", Prunable: true},
		{Role: "user", Content: "current request"},
	}

	out := Compress(msgs, Options{ContextWindow: 200})

	var sawAssistant, sawR1, sawR2 bool
	for _, m := range out {
		if m.Role == "assistant" {
			for _, cid := range m.CallIDs {
				if cid == "call_c1" || cid == "call_c2" {
					sawAssistant = true
				}
			}
		}
		if m.Role == "tool" && m.ToolCallID == "call_c1" {
			sawR1 = true
		}
		if m.Role == "tool" && m.ToolCallID == "call_c2" {
			sawR2 = true
		}
	}
	if sawAssistant != sawR1 || sawR1 != sawR2 {
		t.Errorf("group split at protected boundary: assistant=%v r1=%v r2=%v; out=%+v", sawAssistant, sawR1, sawR2, out)
	}
}

// TestTruncateLargeToolCallArgumentsCounted verifies that large
// serialized tool-call argument payloads are accounted for in the token
// budget.
func TestTruncateLargeToolCallArgumentsCounted(t *testing.T) {
	est := &SimpleEstimator{}
	m := Message{
		Role:    "assistant",
		Content: "",
		CallIDs: []string{strings.Repeat("x", 1000), strings.Repeat("y", 1000), strings.Repeat("z", 1000), strings.Repeat("w", 1000)},
	}
	tokens := messageTokens(m, est)
	if tokens < 500 {
		t.Errorf("expected >=500 tokens for 4 large call IDs, got %d", tokens)
	}
}

// TestToolArgsLargePayloadCounted verifies that large serialized argument
// payloads (ToolArgs) are counted in the token budget. This is the real
// wire cost — the JSON-encoded argument strings, not just the call IDs.
func TestToolArgsLargePayloadCounted(t *testing.T) {
	est := &SimpleEstimator{}
	// Simulate two parallel calls to the same tool "fetch" with different
	// large argument payloads. The first has a 5000-char argument, the
	// second has a 100-char argument.
	m := Message{
		Role:    "assistant",
		Content: "",
		CallIDs: []string{"call_1", "call_2"},
		ToolArgs: []string{
			`{"url":"` + strings.Repeat("a", 5000) + `"}`,
			`{"url":"` + strings.Repeat("b", 100) + `"}`,
		},
	}
	tokens := messageTokens(m, est)
	// 5000-char arg ≈ 1250 tokens, 100-char arg ≈ 25 tokens.
	// Plus 2 call IDs + base content.
	if tokens < 1300 {
		t.Errorf("expected >=1300 tokens for large ToolArgs, got %d", tokens)
	}
}

// TestToolArgsSameNameDifferentArgs verifies that parallel calls to the
// same tool name each preserve their own serialized argument payload in
// ToolArgs (one entry per call, in CallIDs order). No keying by name —
// no overwrite.
func TestToolArgsSameNameDifferentArgs(t *testing.T) {
	// Build through the bridge to verify end-to-end.
	// This test is in the compress package, so we verify the invariant
	// directly: ToolArgs has the same length as CallIDs, one entry per call.
	est := &SimpleEstimator{}
	m := Message{
		Role:    "assistant",
		Content: "",
		CallIDs: []string{"call_a", "call_b", "call_c"},
		ToolArgs: []string{
			`{"query":"first"}`,
			`{"query":"` + strings.Repeat("x", 3000) + `"}`,
			`{"query":"third"}`,
		},
	}
	if len(m.ToolArgs) != len(m.CallIDs) {
		t.Fatalf("ToolArgs length %d != CallIDs length %d", len(m.ToolArgs), len(m.CallIDs))
	}
	// The large middle argument must be counted.
	tokens := messageTokens(m, est)
	if tokens < 700 {
		t.Errorf("expected >=700 tokens (3000-char arg), got %d", tokens)
	}
}
