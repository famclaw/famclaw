package agentcore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

// TestCompressionRegressionEndToEnd captures the wire-level invariant
// that issue #442 targets: after compression, every tool result in the
// serialized request must reference a tool-call ID that is present in
// some assistant message's tool_calls, and every retained assistant
// tool-call ID must have a matching tool result.
//
// This test drives the real stage_tool_loop path with a tight context
// window and a history containing a complete tool-call group. It
// captures the exact bytes sent to the LLM and verifies the group
// integrity invariant on the wire.
//
// RED on baseline: the old compressToLLM uses Role+Content matching.
// When a large historical tool result (bigResult) is evicted, the
// matching assistant message (call_hist_1, empty Content) is retained
// because another empty-Content assistant message matches it first.
// The retained call_hist_1 has no tool result — orphaned call on wire.
func TestCompressionRegressionEndToEnd(t *testing.T) {
	type capturedReq struct {
		Messages []llm.Message `json:"messages"`
	}

	var (
		mu         sync.Mutex
		captured   []capturedReq
		reqCount   int
		decodeErrs []error
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqCount++
		n := reqCount
		mu.Unlock()

		var body capturedReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			mu.Lock()
			decodeErrs = append(decodeErrs, err)
			mu.Unlock()
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}

		mu.Lock()
		captured = append(captured, body)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// First call: respond with a tool call so the loop continues.
			resp := map[string]any{
				"choices": []map[string]any{
					{
						"message": map[string]any{
							"role": "assistant",
							"tool_calls": []map[string]any{
								{
									"id":   "call_e2e_1",
									"type": "function",
									"function": map[string]any{
										"name":      "builtin__echo",
										"arguments": `{"text":"hello"}`,
									},
								},
							},
						},
						"finish_reason": "tool_calls",
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		// Subsequent calls: final answer.
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message":       map[string]any{"role": "assistant", "content": "done"},
					"finish_reason": "stop",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	bigResult := strings.Repeat("Z", 4000)
	turn := &Turn{
		User:  &config.UserConfig{Name: "regress"},
		Tools: []Tool{{Name: "builtin__echo"}},
	}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{
		{
			ID:       "call_new_e2e",
			Function: llm.ToolCallFunction{Name: "builtin__echo", Arguments: map[string]any{"text": "new"}},
		},
	})
	turn.SetMeta("llm_messages", []llm.Message{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "first question"},
		{
			Role: "assistant", Content: "",
			ToolCalls: []llm.ToolCall{
				{ID: "call_hist_1", Function: llm.ToolCallFunction{Name: "builtin__echo", Arguments: map[string]any{"text": "old"}}},
			},
		},
		{Role: "tool", Content: bigResult, ToolCallID: "call_hist_1"},
		{Role: "user", Content: "second question"},
		{Role: "assistant", Content: "old reply"},
	})

	var toolExecs []string
	deps := ToolLoopDeps{
		ClientFactory: func(*Turn) llm.Chatter {
			return llm.NewClient(server.URL, "test", "")
		},
		BuiltinHandler: func(ctx context.Context, name string, args map[string]any) (string, error) {
			toolExecs = append(toolExecs, name)
			return "echoed", nil
		},
		MaxIterations: 3,
		ContextWindow: 600,
	}
	stage := NewStageToolLoop(deps)

	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("stage: %v", err)
	}

	// Fail on JSON decode errors in captured requests.
	mu.Lock()
	dErrs := decodeErrs
	reqs := captured
	mu.Unlock()
	for i, e := range dErrs {
		t.Errorf("request %d: JSON decode error: %v", i, e)
	}
	if len(reqs) == 0 {
		t.Fatalf("no LLM requests captured")
	}

	// Verify the tool was actually executed.
	if len(toolExecs) == 0 {
		t.Errorf("no tool executions captured; expected at least one builtin__echo call")
	}

	// Track which call IDs and results we've positively confirmed.
	var sawHist1, sawHistResult, sawE2e1, sawE2eResult bool
	var sawNewE2e, sawNewE2eResult bool

	for reqIdx, req := range reqs {
		if len(req.Messages) == 0 {
			t.Errorf("request %d: empty messages slice", reqIdx)
			continue
		}

		// Build the set of assistant call IDs in this request.
		assistantIDs := make(map[string]bool)
		for _, m := range req.Messages {
			if m.Role == "assistant" {
				for _, tc := range m.ToolCalls {
					assistantIDs[tc.ID] = true
				}
			}
		}

		// Build the set of tool result IDs in this request.
		resultIDs := make(map[string]bool)
		for _, m := range req.Messages {
			if m.Role == "tool" && m.ToolCallID != "" {
				resultIDs[m.ToolCallID] = true
			}
		}

		// Direction 1: every tool result must reference an assistant call.
		for rid := range resultIDs {
			if !assistantIDs[rid] {
				t.Errorf("request %d: orphaned tool result tool_call_id=%q (no matching assistant tool_call)", reqIdx, rid)
			}
		}

		// Direction 2: every retained assistant call must have a matching result.
		for aid := range assistantIDs {
			if !resultIDs[aid] {
				t.Errorf("request %d: orphaned assistant call id=%q (no matching tool result)", reqIdx, aid)
			}
		}

		// Positive evidence: confirm expected IDs appear.
		if assistantIDs["call_hist_1"] {
			sawHist1 = true
		}
		if resultIDs["call_hist_1"] {
			sawHistResult = true
		}
		if assistantIDs["call_e2e_1"] {
			sawE2e1 = true
		}
		if resultIDs["call_e2e_1"] {
			sawE2eResult = true
		}
		if assistantIDs["call_new_e2e"] {
			sawNewE2e = true
		}
		if resultIDs["call_new_e2e"] {
			sawNewE2eResult = true
		}
	}

	// The historical group must be paired (both call and result present).
	if sawHist1 && !sawHistResult {
		t.Errorf("call_hist_1 assistant retained in some request but no matching tool result found — group split")
	}
	// The in-flight call from the fake LLM response must be paired.
	if sawE2e1 && !sawE2eResult {
		t.Errorf("call_e2e_1 assistant retained but no matching tool result — in-flight group split")
	}
	// The pending_tool_calls entry must be paired.
	if sawNewE2e && !sawNewE2eResult {
		t.Errorf("call_new_e2e assistant retained but no matching tool result — pending group split")
	}
}

// TestCompressionRegressionSelectiveEviction drives the real
// stage_tool_loop path with a tight context window and a history
// containing two tool-call groups with distinct call IDs. The
// compression must evict whole groups, not split them: either both the
// assistant call AND its tool result are present, or neither is.
//
// This is the same regression as TestCompressionRegressionEndToEnd but
// with two distinct groups (call_g1 and call_g2) to verify that
// selective eviction of one group does not corrupt the other.
func TestCompressionRegressionSelectiveEviction(t *testing.T) {
	type capturedReq struct {
		Messages []llm.Message `json:"messages"`
	}

	var (
		mu         sync.Mutex
		captured   []capturedReq
		reqCount   int
		decodeErrs []error
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqCount++
		n := reqCount
		mu.Unlock()

		var body capturedReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			mu.Lock()
			decodeErrs = append(decodeErrs, err)
			mu.Unlock()
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}

		mu.Lock()
		captured = append(captured, body)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			resp := map[string]any{
				"choices": []map[string]any{
					{
						"message": map[string]any{
							"role": "assistant",
							"tool_calls": []map[string]any{
								{
									"id":   "call_sel_1",
									"type": "function",
									"function": map[string]any{
										"name":      "builtin__echo",
										"arguments": `{"text":"sel"}`,
									},
								},
							},
						},
						"finish_reason": "tool_calls",
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message":       map[string]any{"role": "assistant", "content": "done"},
					"finish_reason": "stop",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Two historical tool-call groups: one large (should be evicted),
	// one small (should survive positive assertion).
	big1 := strings.Repeat("A", 3000)  // ~750 tokens — will be evicted
	small2 := strings.Repeat("B", 200) // ~50 tokens — should survive

	turn := &Turn{
		User:  &config.UserConfig{Name: "sel"},
		Tools: []Tool{{Name: "builtin__echo"}},
	}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{
		{
			ID:       "call_new_sel",
			Function: llm.ToolCallFunction{Name: "builtin__echo", Arguments: map[string]any{"text": "new"}},
		},
	})
	turn.SetMeta("llm_messages", []llm.Message{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "first question"},
		// Group 1: assistant with call_g1 + large result
		{
			Role: "assistant", Content: "",
			ToolCalls: []llm.ToolCall{
				{ID: "call_g1", Function: llm.ToolCallFunction{Name: "builtin__echo", Arguments: map[string]any{"text": "old1"}}},
			},
		},
		{Role: "tool", Content: big1, ToolCallID: "call_g1"},
		// Group 2: assistant with call_g2 + large result
		{Role: "user", Content: "second question"},
		{
			Role: "assistant", Content: "",
			ToolCalls: []llm.ToolCall{
				{ID: "call_g2", Function: llm.ToolCallFunction{Name: "builtin__echo", Arguments: map[string]any{"text": "old2"}}},
			},
		},
		{Role: "tool", Content: small2, ToolCallID: "call_g2"},
		{Role: "user", Content: "third question"},
		{Role: "assistant", Content: "old reply"},
	})

	deps := ToolLoopDeps{
		ClientFactory: func(*Turn) llm.Chatter {
			return llm.NewClient(server.URL, "test", "")
		},
		BuiltinHandler: func(ctx context.Context, name string, args map[string]any) (string, error) {
			return "echoed", nil
		},
		MaxIterations: 3,
		ContextWindow: 600,
	}
	stage := NewStageToolLoop(deps)

	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("stage: %v", err)
	}

	mu.Lock()
	dErrs := decodeErrs
	reqs := captured
	mu.Unlock()
	for i, e := range dErrs {
		t.Errorf("request %d: JSON decode error: %v", i, e)
	}
	if len(reqs) == 0 {
		t.Fatalf("no LLM requests captured")
	}

	for reqIdx, req := range reqs {
		if len(req.Messages) == 0 {
			t.Errorf("request %d: empty messages slice", reqIdx)
			continue
		}

		// Build the set of assistant call IDs and tool result IDs.
		assistantIDs := make(map[string]bool)
		resultIDs := make(map[string]bool)
		for _, m := range req.Messages {
			if m.Role == "assistant" {
				for _, tc := range m.ToolCalls {
					assistantIDs[tc.ID] = true
				}
			}
			if m.Role == "tool" && m.ToolCallID != "" {
				resultIDs[m.ToolCallID] = true
			}
		}

		// Direction 1: every tool result must reference an assistant call.
		for rid := range resultIDs {
			if !assistantIDs[rid] {
				t.Errorf("request %d: orphaned tool result tool_call_id=%q", reqIdx, rid)
			}
		}

		// Direction 2: every retained assistant call must have a matching result.
		for aid := range assistantIDs {
			if !resultIDs[aid] {
				t.Errorf("request %d: orphaned assistant call id=%q", reqIdx, aid)
			}
		}

		// Verify each historical group is either fully present or fully absent.
		for _, gid := range []string{"call_g1", "call_g2"} {
			a := assistantIDs[gid]
			r := resultIDs[gid]
			if a != r {
				t.Errorf("request %d: group %q split: assistant=%v result=%v", reqIdx, gid, a, r)
			}
		}
	}

	// Positive survival: the small group (call_g2) must survive in
	// at least one request. The big group (call_g1) must be evicted
	// from at least one request. This prevents the test from passing
	// when BOTH groups disappear.
	sawSmall := false
	sawBigEvicted := false
	for _, req := range reqs {
		aid := make(map[string]bool)
		for _, m := range req.Messages {
			if m.Role == "assistant" {
				for _, tc := range m.ToolCalls {
					aid[tc.ID] = true
				}
			}
		}
		if aid["call_g2"] {
			sawSmall = true
		}
		if !aid["call_g1"] {
			sawBigEvicted = true
		}
	}
	if !sawSmall {
		t.Error("positive survival: call_g2 (small group) was never retained — both groups vanished")
	}
	if !sawBigEvicted {
		t.Error("eviction: call_g1 (big group) was never evicted — budget not applied")
	}
}

// TestStageCompressToLLMRoundTrip verifies that ToolCallID on tool-role
// messages in agentcore.Message survives the full pipeline:
// StageCompress → turnToLLMMessages → serialized HTTP request.
//
// Without this propagation, the wire request would contain tool results
// with no tool_call_id, breaking OpenAI wire invariants.
//
// Setup: two tool-call rounds with distinct IDs. The first round's group
// is large enough to be evicted; the second round's group is small and
// must survive. After compression + turnToLLMMessages, the retained tool
// result must carry its ToolCallID in the serialized JSON.
func TestStageCompressToLLMRoundTrip(t *testing.T) {
	// Build a turn with:
	//   Round 1 (old, big): assistant calls "call_rt1" → tool result (2000 chars)
	//   Round 2 (new, small): assistant calls "call_rt2" → tool result (100 chars)
	turn := &Turn{
		Messages: []Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "history question"},
			{
				Role: "assistant", Content: "",
				ToolCalls: []ToolCall{
					{ID: "call_rt1", Function: ToolCallFunction{Name: "search", Arguments: map[string]any{"q": "old"}}},
				},
			},
			{Role: "tool", Content: strings.Repeat("R", 2000), ToolCallID: "call_rt1"},
			{Role: "user", Content: "new question"},
			{
				Role: "assistant", Content: "",
				ToolCalls: []ToolCall{
					{ID: "call_rt2", Function: ToolCallFunction{Name: "calc", Arguments: map[string]any{"x": 42}}},
				},
			},
			{Role: "tool", Content: "result42", ToolCallID: "call_rt2"},
			{Role: "user", Content: "follow up"},
			{Role: "assistant", Content: "final answer"},
		},
	}

	// Force eviction of round 1 (big) but retention of round 2 (small).
	// Budget: system(~1) + history q(~5) + round1 group(~505) + new q(~5)
	// + round2 group(~25) + follow up(~4) + final(~5) ≈ 550 tokens total.
	// With budget=300, the big round 1 group must be evicted.
	// The last 3 (follow up, final answer) and system are protected.
	// Round 2 (new, small) should survive.
	stage := NewStageCompress(300)
	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("StageCompress: %v", err)
	}

	// Convert to LLM messages (the actual outgoing wire path).
	llmMsgs := turnToLLMMessages(turn.Messages)

	// Verify: if call_rt2's tool result is present, it must carry ToolCallID.
	sawResultRT2 := false
	for _, m := range llmMsgs {
		if m.Role == "tool" && m.Content == "result42" {
			sawResultRT2 = true
			if m.ToolCallID != "call_rt2" {
				t.Errorf("tool result 'result42' lost ToolCallID after compression+conversion; got %q", m.ToolCallID)
			}
		}
	}
	// Round 2 should survive (small group, recent).
	if !sawResultRT2 {
		t.Error("round 2 tool result 'result42' was evicted; expected retention (small group, recent)")
	}

	// Verify: if call_rt1's tool result is evicted, its assistant must also be gone.
	sawResultRT1 := false
	sawCallRT1 := false
	for i, m := range llmMsgs {
		if m.Role == "tool" && m.ToolCallID == "call_rt1" {
			sawResultRT1 = true
		}
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if tc.ID == "call_rt1" {
					sawCallRT1 = true
				}
			}
		}
		_ = i
	}
	if sawResultRT1 != sawCallRT1 {
		t.Errorf("round 1 split: callRT1=%v resultRT1=%v — group must be atomic", sawCallRT1, sawResultRT1)
	}

	// Bidirectional wire invariant on final messages.
	assistantIDs := make(map[string]bool)
	resultIDs := make(map[string]bool)
	for _, m := range llmMsgs {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				assistantIDs[tc.ID] = true
			}
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			resultIDs[m.ToolCallID] = true
		}
	}
	for rid := range resultIDs {
		if !assistantIDs[rid] {
			t.Errorf("wire: orphaned tool_call_id=%q (no matching assistant call)", rid)
		}
	}
	for aid := range assistantIDs {
		if !resultIDs[aid] {
			t.Errorf("wire: orphaned assistant call ID=%q (no matching tool result)", aid)
		}
	}
}

// TestStageCompressEvictionOldRoundPreservedNewRound verifies the reused-ID
// boundary in buildGroups: when the same tool-call ID appears in two
// separate rounds, eviction must remove the entire old round pair while
// preserving the new round pair at their exact positions.
//
// The fixture:
//
//	[system, user_old, assistant_old(call_X), result_old(call_X, big),
//	 user_new, assistant_new(call_X), result_new(call_X, small),
//	 user_cur, assistant_cur]
//
// With a tight budget, the old group (assistant_old + result_old) must be
// evicted as a unit. The new group (assistant_new + result_new) must be
// retained. If the old assistant incorrectly claims result_new, the new
// group becomes orphaned or split.
func TestStageCompressEvictionOldRoundPreservedNewRound(t *testing.T) {
	// Use the same ID "call_reused" in both rounds.
	oldResult := strings.Repeat("O", 2000) // big — will be evicted
	newResult := "N42"                     // small — must survive

	turn := &Turn{
		Messages: []Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "old question"},
			{
				Role: "assistant", Content: "",
				ToolCalls: []ToolCall{
					{ID: "call_reused", Function: ToolCallFunction{Name: "probe", Arguments: map[string]any{"round": 1}}},
				},
			},
			{Role: "tool", Content: oldResult, ToolCallID: "call_reused"},
			{Role: "user", Content: "new question"},
			{
				Role: "assistant", Content: "",
				ToolCalls: []ToolCall{
					{ID: "call_reused", Function: ToolCallFunction{Name: "probe", Arguments: map[string]any{"round": 2}}},
				},
			},
			{Role: "tool", Content: newResult, ToolCallID: "call_reused"},
			{Role: "user", Content: "current question"},
			{Role: "assistant", Content: "current answer"},
		},
	}

	// Budget forces eviction of the old group (~505 tokens) but retains
	// the new group (~10 tokens). System + last 3 are protected.
	stage := NewStageCompress(250)
	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("StageCompress: %v", err)
	}

	// Build LLM messages for wire check.
	llmMsgs := turnToLLMMessages(turn.Messages)

	// The old result (2000 chars of 'O') must be evicted.
	for _, m := range llmMsgs {
		if m.Role == "tool" && m.Content == oldResult {
			t.Errorf("old round tool result (big, call_reused) was not evicted; budget=250")
		}
	}

	// The new result ("N42") must survive with correct ToolCallID.
	sawNew := false
	for _, m := range llmMsgs {
		if m.Role == "tool" && m.Content == newResult {
			sawNew = true
			if m.ToolCallID != "call_reused" {
				t.Errorf("new round tool result lost ToolCallID; got %q", m.ToolCallID)
			}
		}
	}
	if !sawNew {
		t.Error("new round tool result 'N42' was evicted; expected retention (small, recent)")
	}

	// The new round's assistant must also survive (atomic group).
	sawNewAssistant := false
	for _, m := range llmMsgs {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if tc.ID == "call_reused" {
					if args, ok := tc.Function.Arguments["round"]; ok {
						if n, isInt := args.(int); isInt && n == 2 {
							sawNewAssistant = true
						}
					}
				}
			}
		}
	}
	if !sawNewAssistant {
		t.Error("new round assistant (call_reused, round=2) was evicted; expected retention with its result")
	}

	// The old round's assistant must be evicted (its group was evicted).
	sawOldAssistant := false
	for _, m := range llmMsgs {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if tc.ID == "call_reused" {
					if args, ok := tc.Function.Arguments["round"]; ok {
						if n, isInt := args.(int); isInt && n == 1 {
							sawOldAssistant = true
						}
					}
				}
			}
		}
	}
	if sawOldAssistant {
		t.Error("old round assistant (call_reused, round=1) survived eviction; expected removal with its big result")
	}

	// Bidirectional wire invariant.
	assistantIDs := make(map[string]bool)
	resultIDs := make(map[string]bool)
	for _, m := range llmMsgs {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				assistantIDs[tc.ID] = true
			}
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			resultIDs[m.ToolCallID] = true
		}
	}
	// Note: both old and new use "call_reused", so set-based checks alone
	// cannot distinguish. We already verified above by content/args that
	// old is gone and new is present. This check ensures no orphan from
	// the new round.
	for rid := range resultIDs {
		if !assistantIDs[rid] {
			t.Errorf("wire: orphaned tool_call_id=%q", rid)
		}
	}
}
