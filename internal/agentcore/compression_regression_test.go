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

	// Two historical tool-call groups, each with a large prunable result.
	big1 := strings.Repeat("A", 3000)
	big2 := strings.Repeat("B", 3000)

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
		{Role: "tool", Content: big2, ToolCallID: "call_g2"},
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
}

