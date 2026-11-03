package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/websearch"
)

// These tests cover the stale-search guards (Fix 1 and Fix 3): the pure
// intent/claim predicates and the combined shouldFreshSearchRetry decision,
// plus the extracted gateOutput helper. They are written against the exact
// failure signature from the butler-stale-search root-cause report: the model
// emits "I can't access/check/search" WITHOUT calling web_search, then repeats
// it when the user says "try search again".

func TestIsRetryIntent(t *testing.T) {
	positives := []string{
		"Now try search again",
		"try again",
		"Try that one more time",
		"search again please",
		"run the search again",
		"check it again",
		"retry",
	}
	for _, in := range positives {
		if !isRetryIntent(in) {
			t.Errorf("isRetryIntent(%q) = false, want true", in)
		}
	}
	negatives := []string{
		"what's the weather today",
		"try a different tone",
		"look up the capital of France",
		"I need to look into a refund",
		"search for news about AI", // asks to search, not to retry a failed one
		"hello",
	}
	for _, in := range negatives {
		if isRetryIntent(in) {
			t.Errorf("isRetryIntent(%q) = true, want false", in)
		}
	}
}

func TestHasSearchFailureClaim(t *testing.T) {
	positives := []string{
		"I can't access that.",
		"I'm unable to check the current exchange rate.",
		"Unfortunately I don't have access to live search.",
		"I couldn't search for that because I have no web tool.",
		"I cannot look that up without being able to search.",
	}
	for _, in := range positives {
		if !hasSearchFailureClaim(in) {
			t.Errorf("hasSearchFailureClaim(%q) = false, want true", in)
		}
	}
	negatives := []string{
		"The current rate is 1.08 USD/JPY.",
		"I searched and found no results for that query.", // legitimate: a search WAS recorded
		"Here is the answer you asked for.",
		"I can help with that — let me look it up.",
		// Review case: a generic refusal that is NOT about search must not
		// trigger the stale-search retry, even when web_search is available.
		"I can't do that without being able to access the database.",
	}
	for _, in := range negatives {
		if hasSearchFailureClaim(in) {
			t.Errorf("hasSearchFailureClaim(%q) = true, want false", in)
		}
	}
}

func TestTrailingHistoryHasFailureClaim(t *testing.T) {
	if trailingHistoryHasFailureClaim(nil) {
		t.Error("nil history should be false")
	}
	if trailingHistoryHasFailureClaim([]*store.Message{{Role: "user", Content: "hello"}}) {
		t.Error("history with no failure claim should be false")
	}
	// A failure claim in an assistant message within the last 4 should fire.
	hist := []*store.Message{
		{Role: "user", Content: "What's the latest rate?"},
		{Role: "assistant", Content: "I can't access that."},
		{Role: "user", Content: "Now try search again"},
	}
	if !trailingHistoryHasFailureClaim(hist) {
		t.Error("trailing failure claim should be detected")
	}
	// A claim older than the last 4 should NOT fire (it's stale context, not a
	// recent failed answer).
	older := []*store.Message{
		{Role: "user", Content: "old"},
		{Role: "assistant", Content: "I can't access that."},
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
		{Role: "user", Content: "Now try search again"},
	}
	if trailingHistoryHasFailureClaim(older) {
		t.Error("failure claim older than the trailing window should not be detected")
	}
}

func TestWebSearchAvailableAndRecorded(t *testing.T) {
	turn := &agentcore.Turn{
		Tools: []agentcore.Tool{
			{Name: "builtin__spawn_agent"},
			{Name: "builtin__web_search"},
		},
	}
	if !webSearchAvailable(turn) {
		t.Error("web_search in Tools should be reported available")
	}
	noSearch := &agentcore.Turn{Tools: []agentcore.Tool{{Name: "builtin__spawn_agent"}}}
	if webSearchAvailable(noSearch) {
		t.Error("no web_search in Tools should be reported unavailable")
	}

	if recordedWebSearchThisTurn(nil) {
		t.Error("no results should not count as recorded")
	}
	if recordedWebSearchThisTurn([]agentcore.ToolResult{{ToolName: "builtin__spawn_agent"}}) {
		t.Error("non-web_search result should not count as recorded")
	}
	if !recordedWebSearchThisTurn([]agentcore.ToolResult{{ToolName: "builtin__web_search"}}) {
		t.Error("web_search result should count as recorded")
	}
}

// TestShouldFreshSearchRetry exercises the combined predicate that drives the
// one-shot re-prompt.
func TestShouldFreshSearchRetry(t *testing.T) {
	turn := &agentcore.Turn{
		Tools:  []agentcore.Tool{{Name: "builtin__web_search"}},
		Output: "I can't access that.",
	}
	if !(&Agent{}).shouldFreshSearchRetry(turn) {
		t.Error("claim + available + no record should fire the retry")
	}

	// A real web_search result recorded this turn suppresses the retry.
	turn.ToolCalls = []agentcore.ToolResult{{ToolName: "builtin__web_search"}}
	if (&Agent{}).shouldFreshSearchRetry(turn) {
		t.Error("recorded web_search result should suppress the retry")
	}

	// A legitimate "searched, found nothing" answer (no failure claim) does not
	// fire.
	turn.Output = "I searched and found no results."
	turn.ToolCalls = nil
	if (&Agent{}).shouldFreshSearchRetry(turn) {
		t.Error("no failure claim should not fire the retry")
	}

	// web_search not offered does not fire.
	turn.Output = "I can't access that."
	turn.Tools = []agentcore.Tool{{Name: "builtin__spawn_agent"}}
	if (&Agent{}).shouldFreshSearchRetry(turn) {
		t.Error("web_search unavailable should not fire the retry")
	}
}

// TestGateOutput_AllowedAndNil verifies the extracted gate helper: a nil
// evaluator is a no-op, and an allowed output is sanitized in place.
func TestGateOutput_AllowedAndNil(t *testing.T) {
	// Nil evaluator -> no gate, done=false.
	if _, done := (&Agent{}).gateOutput(context.Background(), &agentcore.Turn{Output: "hello"}); done {
		t.Error("nil evaluator should not report done")
	}

	a := setupAgent(t, "http://127.0.0.1:1")
	turn := &agentcore.Turn{Output: "here is a fine answer"}
	if _, done := a.gateOutput(context.Background(), turn); done {
		t.Error("allowed output should not report done")
	}
	if turn.Output == "" {
		t.Error("allowed output should remain in turn.Output")
	}
}

// staleSearchChatter is a scripted llm.Chatter for the end-to-end
// stale-search regression test. It serves responses in order and records how
// many times ChatWithTools was invoked. This mirrors the incident: on the
// retry turn the model first emits a stale refusal without calling web_search.
type staleSearchChatter struct {
	script   []*llm.Message
	calls    int
	recorded [][]llm.Message
}

func (m *staleSearchChatter) Chat(context.Context, []llm.Message, float64, int, func(string)) (string, error) {
	return "", errors.New("staleSearchChatter.Chat: unexpected")
}

func (m *staleSearchChatter) ChatMessage(context.Context, []llm.Message, float64, int) (*llm.Message, error) {
	return nil, errors.New("staleSearchChatter.ChatMessage: unexpected")
}

func (m *staleSearchChatter) ChatSync(context.Context, []llm.Message, float64, int) (string, error) {
	return "", errors.New("staleSearchChatter.ChatSync: unexpected")
}

func (m *staleSearchChatter) ChatWithTools(_ context.Context, msgs []llm.Message, _ float64, _ int, _ []llm.ToolDef) (*llm.Message, error) {
	idx := m.calls
	m.calls++
	m.recorded = append(m.recorded, msgs)
	if idx >= len(m.script) {
		idx = len(m.script) - 1
	}
	return m.script[idx], nil
}

// TestStaleSearchFailRecoverRetryEndToEnd is the offline regression for the
// butler stale-search bug (kanban acceptance #3). It reproduces the full
// fail -> recover -> explicit-retry flow with a scripted LLM and an
// httptest SearXNG endpoint:
//
//  1. A prior turn records a search failure ("I couldn't check ... search is
//     still broken").
//  2. The user explicitly retries ("Search again").
//  3. The model FIRST emits a stale refusal without calling web_search.
//  4. The fresh-search guard re-prompts; the model runs a real web_search
//     against the httptest SearXNG endpoint and returns a fresh answer.
//
// Before the fix there was no guard, so step 4 never happened: the stale
// refusal was delivered as-is, the LLM was called once, and the SearXNG
// endpoint was never hit. This test therefore FAILS without the fix and
// PASSES with it. It is fully offline (no live inference, no real SearXNG).
func TestStaleSearchFailRecoverRetryEndToEnd(t *testing.T) {
	t.Parallel()

	// Offline SearXNG endpoint returning one fresh result.
	var searxngHits int32
	searxng := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&searxngHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"results":[{"title":"Fresh","url":"https://example.com/fresh","content":"clear skies, 72F"}]}`)
	}))
	defer searxng.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	clf := classifier.New()

	// Scripted LLM: [0] stale refusal (no tool call), [1] web_search tool
	// call, [2] fresh final answer.
	mock := &staleSearchChatter{script: []*llm.Message{
		{Role: "assistant", Content: "I can't check that right now; the search is still broken."},
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:       "call_ws_1",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "weather today"}},
			}},
		},
		{Role: "assistant", Content: "Here's the latest from a fresh search: clear skies, 72°F."},
	}}

	cfg := &config.Config{
		LLM: config.LLMConfig{
			Temperature:       0.7,
			MaxResponseTokens: 200,
			MaxContextTokens:  8000,
		},
		Users: []config.UserConfig{{Name: "parent", Role: "parent"}},
		Tools: config.ToolsConfig{
			WebSearch: config.WebSearchConfig{
				Enabled:    true,
				Provider:   "searxng",
				Endpoint:   searxng.URL,
				MaxResults: 5,
				TimeoutSec: 5,
			},
			WebFetch: config.WebFetchConfig{
				URLAllowlist: []string{"127.0.0.1"},
			},
		},
	}
	user := &cfg.Users[0]

	agent, err := NewAgent(user, cfg, mock, ev, clf, db, AgentDeps{
		MsgContext:   gateway.MsgContext{ConvID: "test-conv-1"},
		BuiltinTools: []agentcore.Tool{websearch.Tool([]string{"parent"})},
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	// Seed the PRIOR failed turn into the same conversation so the retry turn
	// sees a trailing search failure-claim (Fix 1 precondition).
	if err := db.SaveMessage("test-conv-1", "parent", "user", "What's the weather?", "", "", ""); err != nil {
		t.Fatalf("seed user msg: %v", err)
	}
	if err := db.SaveMessage("test-conv-1", "parent", "assistant", "I couldn't check the weather; the search is still broken.", "", "allow", ""); err != nil {
		t.Fatalf("seed assistant msg: %v", err)
	}

	resp, err := agent.Chat(context.Background(), "Search again", func(string) {})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// Recovery: the delivered answer is the FRESH one, not the stale refusal.
	if !strings.Contains(resp.Content, "fresh search") {
		t.Fatalf("Content = %q — expected the recovered fresh answer, got a stale refusal", resp.Content)
	}
	if strings.Contains(resp.Content, "still broken") {
		t.Errorf("Content = %q — stale refusal was delivered (guard did not recover)", resp.Content)
	}

	// The guard re-prompted: LLM called 3 times (refusal -> tool call -> final).
	if mock.calls != 3 {
		t.Errorf("ChatWithTools calls = %d, want 3 (stale refusal -> web_search -> fresh final)", mock.calls)
	}

	// A REAL web_search was executed against the offline endpoint.
	if got := atomic.LoadInt32(&searxngHits); got != 1 {
		t.Errorf("SearXNG hits = %d, want 1 (a fresh search was actually run)", got)
	}
}

// TestToollessRetryClearsStaleToolMeta is the F1 regression test: when a
// reused Turn is re-run for a tool-less retry (the fresh-search re-prompt in
// agent.go), the LLM stage must clear the stale pending_tool_calls +
// llm_messages left over from the previous attempt, otherwise
// StageToolLoop re-executes the old batch against the new, tool-less answer.
// This test FAILS on a version without the turn.DeleteMeta clear in the
// no-tool-calls branch of agentcore/stage_llm_call.go and PASSES with it.
func TestToollessRetryClearsStaleToolMeta(t *testing.T) {
	// Script: [0] the model issues a web_search tool call (first attempt),
	// [1] the model answers with a plain tool-less response (retry).
	mock := &staleSearchChatter{script: []*llm.Message{
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:       "call_f1_1",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "weather today"}},
			}},
		},
		{Role: "assistant", Content: "Here's the answer from the fresh tool-less run."},
	}}

	stage := agentcore.NewStageLLMCall(agentcore.LLMCallDeps{
		ClientFactory: func(*agentcore.Turn) llm.Chatter { return mock },
		Temperature:   0.0,
		MaxTokens:     512,
	})

	// A single Turn object, reused across both stage runs — exactly what the
	// agent's fresh-search re-prompt does.
	turn := &agentcore.Turn{
		User:     &config.UserConfig{Name: "parent", Role: "parent"},
		Tools:    []agentcore.Tool{{Name: "builtin__web_search"}},
		Messages: []agentcore.Message{{Role: "user", Content: "what's the weather?"}},
	}

	// First attempt: the response carries a tool call, so the stage stores
	// the tool-loop handoff state.
	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("first stage run: %v", err)
	}
	if _, ok := turn.GetMeta("pending_tool_calls"); !ok {
		t.Fatal("pending_tool_calls should be set after a response with tool calls")
	}
	if _, ok := turn.GetMeta("llm_messages"); !ok {
		t.Fatal("llm_messages should be set after a response with tool calls")
	}

	// Tool-less retry on the same reused Turn: the stage must clear the
	// stale tool-loop state so StageToolLoop does not re-inject the batch.
	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("tool-less retry stage run: %v", err)
	}
	if _, ok := turn.GetMeta("pending_tool_calls"); ok {
		t.Error("pending_tool_calls should be cleared on a tool-less retry (stale batch would be re-injected)")
	}
	if _, ok := turn.GetMeta("llm_messages"); ok {
		t.Error("llm_messages should be cleared on a tool-less retry (stale message list would be re-injected)")
	}
	if !strings.Contains(turn.Output, "fresh tool-less run") {
		t.Errorf("turn.Output = %q, want the tool-less retry answer", turn.Output)
	}
	if mock.calls != 2 {
		t.Errorf("ChatWithTools calls = %d, want 2 (first attempt + tool-less retry)", mock.calls)
	}
}

// staleSearchTestCfg builds the standard offline config for the F3 e2e tests:
// a SearXNG endpoint at the given URL, web_search allowed for the parent user.
func staleSearchTestCfg(endpoint string) *config.Config {
	return &config.Config{
		LLM: config.LLMConfig{Temperature: 0.7, MaxResponseTokens: 200, MaxContextTokens: 8000},
		Users: []config.UserConfig{
			{Name: "parent", Role: "parent"},
		},
		Tools: config.ToolsConfig{
			WebSearch: config.WebSearchConfig{
				Enabled:    true,
				Provider:   "searxng",
				Endpoint:   endpoint,
				MaxResults: 5,
				TimeoutSec: 5,
			},
			WebFetch: config.WebFetchConfig{URLAllowlist: []string{"127.0.0.1"}},
		},
	}
}

// buildStaleSearchAgent builds an Agent wired to the given db, scripted
// Chatter, and conversation. withWebSearch=false offers no web_search builtin
// (the search tool simply was not available that turn), which is what
// suppresses the fresh-search re-prompt guard.
func buildStaleSearchAgent(t *testing.T, db *store.DB, cfg *config.Config, mock *staleSearchChatter, convID string, withWebSearch bool) *Agent {
	t.Helper()
	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatalf("policy.NewEvaluator: %v", err)
	}
	var builtins []agentcore.Tool
	if withWebSearch {
		builtins = append(builtins, websearch.Tool([]string{"parent"}))
	}
	agent, err := NewAgent(&cfg.Users[0], cfg, mock, ev, classifier.New(), db, AgentDeps{
		MsgContext:   gateway.MsgContext{ConvID: convID},
		BuiltinTools: builtins,
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	return agent
}

// TestStaleSearchRetryPreservesSeededHistory is F3 (i): the seeded
// conversation history is INTACT after a Chat that triggers the stale-search
// retry — explicit preservation proof via a DB read-back. The retry reuses
// the same convID and the guard re-prompts the turn in-memory; neither path
// may delete, rewrite, or duplicate the stored messages.
func TestStaleSearchRetryPreservesSeededHistory(t *testing.T) {
	t.Parallel()

	// Offline SearXNG endpoint returning one fresh result.
	var searxngHits int32
	searxng := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&searxngHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"results":[{"title":"Fresh","url":"https://example.com/fresh","content":"clear skies, 72F"}]}`)
	}))
	defer searxng.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const convID = "f3-conv-history"

	// Seed the PRIOR failed turn exactly as it would have been recorded.
	seedUser := "What's the weather?"
	seedRefusal := "I couldn't check the weather; the search is still broken."
	if err := db.SaveMessage(convID, "parent", "user", seedUser, "", "", ""); err != nil {
		t.Fatalf("seed user msg: %v", err)
	}
	if err := db.SaveMessage(convID, "parent", "assistant", seedRefusal, "", "allow", ""); err != nil {
		t.Fatalf("seed assistant msg: %v", err)
	}

	mock := &staleSearchChatter{script: []*llm.Message{
		{Role: "assistant", Content: "I can't check that right now; the search is still broken."},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call_f3_i", Type: "function",
			Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "weather today"}}}}},
		{Role: "assistant", Content: "Here's the latest from a fresh search: clear skies, 72°F."},
	}}

	cfg := staleSearchTestCfg(searxng.URL)
	agent := buildStaleSearchAgent(t, db, cfg, mock, convID, true)

	resp, err := agent.Chat(context.Background(), "Search again", func(string) {})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(resp.Content, "fresh search") {
		t.Fatalf("Content = %q — expected the recovered fresh answer", resp.Content)
	}
	if mock.calls != 3 {
		t.Fatalf("ChatWithTools calls = %d, want 3 (stale refusal -> web_search -> fresh final)", mock.calls)
	}

	// F3 (i): read the stored conversation back and prove the seeded history
	// is intact — present, unmodified, exactly once each — with only the new
	// turn appended after it.
	hist, err := db.GetConversationHistory(convID, 100)
	if err != nil {
		t.Fatalf("GetConversationHistory: %v", err)
	}
	counts := make(map[string]int)
	for _, m := range hist {
		counts[m.Role+"	"+m.Content]++
	}
	if got := counts["user	"+seedUser]; got != 1 {
		t.Errorf("seeded user message count = %d, want 1 (stored history was mutated)", got)
	}
	if got := counts["assistant	"+seedRefusal]; got != 1 {
		t.Errorf("seeded assistant refusal count = %d, want 1 (stored history was mutated)", got)
	}
	if got := counts["user	Search again"]; got != 1 {
		t.Errorf("new user message count = %d, want 1", got)
	}
	if got := counts["assistant	"+resp.Content]; got != 1 {
		t.Errorf("new assistant final answer count = %d, want 1", got)
	}
	if len(hist) != 4 {
		t.Errorf("history length = %d, want 4 (2 seeded + new user + new final) — a stored message was deleted or duplicated", len(hist))
	}
}

// TestStaleSearchBoundedSecondFailureDelivered is F3 (ii): when the re-prompted
// attempt 2 ALSO refuses without a tool call, the guard is single-shot — the
// second refusal is delivered as-is, the LLM is called exactly 3x total
// (1 seed turn + refusal + bounded second refusal), and there is no further
// re-prompt.
func TestStaleSearchBoundedSecondFailureDelivered(t *testing.T) {
	t.Parallel()

	// Search endpoint is DOWN for the whole test: no web_search can succeed.
	var searxngHits int32
	searxng := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&searxngHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer searxng.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const convID = "f3-conv-bounded"

	// #417 production wording (the recorded stale refusal), then two more
	// tool-less refusals for the retry turn. The script has EXACTLY three
	// responses, so mock.calls == 3 is also the "no further re-prompt" proof:
	// a fourth call would be clamped onto the last entry and break the count
	// assertion.
	refusal0 := "I'm unable to continue this task without being able to search the web for the latest pricing."
	refusal1 := "I can't check that right now; the search is still broken."
	refusal2 := "I'm sorry, I still can't get that pricing figure for you right now."
	mock := &staleSearchChatter{script: []*llm.Message{
		{Role: "assistant", Content: refusal0},
		{Role: "assistant", Content: refusal1},
		{Role: "assistant", Content: refusal2},
	}}

	cfg := staleSearchTestCfg(searxng.URL)

	// Turn 1: web_search was NOT offered (the backend was down — the butler's
	// original attempt), so the guard cannot re-prompt; the refusal is
	// delivered as-is and recorded. Exactly one LLM call.
	agent1 := buildStaleSearchAgent(t, db, cfg, mock, convID, false)
	resp1, err := agent1.Chat(context.Background(), "What's the latest pricing?", func(string) {})
	if err != nil {
		t.Fatalf("Chat #1: %v", err)
	}
	if resp1.Content != refusal0 {
		t.Fatalf("Chat #1 content = %q, want the refusal delivered as-is", resp1.Content)
	}
	if mock.calls != 1 {
		t.Fatalf("Chat #1 calls = %d, want 1 (no web_search offered -> no re-prompt)", mock.calls)
	}

	// Turn 2: web_search IS offered again. Attempt 1 re-emits a stale refusal
	// with no tool call; the guard re-prompts ONCE; attempt 2 refuses again ->
	// delivered as-is, no further re-prompt.
	agent2 := buildStaleSearchAgent(t, db, cfg, mock, convID, true)
	resp2, err := agent2.Chat(context.Background(), "Search again", func(string) {})
	if err != nil {
		t.Fatalf("Chat #2: %v", err)
	}
	if resp2.Content != refusal2 {
		t.Fatalf("Chat #2 content = %q, want the bounded second failure delivered as-is", resp2.Content)
	}
	if resp2.PolicyAction != "allow" {
		t.Errorf("PolicyAction = %q, want allow", resp2.PolicyAction)
	}
	if mock.calls != 3 {
		t.Errorf("total ChatWithTools calls = %d, want exactly 3 (1 seed + refusal + bounded second refusal; no further re-prompt)", mock.calls)
	}
	if got := atomic.LoadInt32(&searxngHits); got != 0 {
		t.Errorf("SearXNG hits = %d, want 0 (both refusals are tool-less; no search was ever executed)", got)
	}
}

// TestStaleSearchFreshFailureHonestDelivery is F3 (iii): the re-prompted
// attempt 2 DOES call web_search, but the endpoint is offline and errors.
// The recorded tool result suppresses any second re-prompt, and the model's
// final answer reports the fresh failure honestly — it is neither the stale
// refusal nor a fabricated fresh result.
func TestStaleSearchFreshFailureHonestDelivery(t *testing.T) {
	t.Parallel()

	// Offline SearXNG endpoint: the search attempt itself errors (5xx).
	var searxngHits int32
	searxng := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&searxngHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer searxng.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const convID = "f3-conv-fresh"

	// Seed the PRIOR failed turn with the #417 production wording.
	seedUser := "What's the latest pricing?"
	seedRefusal := "I'm unable to continue this task without being able to search the web for the latest pricing."
	if err := db.SaveMessage(convID, "parent", "user", seedUser, "", "", ""); err != nil {
		t.Fatalf("seed user msg: %v", err)
	}
	if err := db.SaveMessage(convID, "parent", "assistant", seedRefusal, "", "allow", ""); err != nil {
		t.Fatalf("seed assistant msg: %v", err)
	}

	// Attempt 1 re-emits the stale refusal without a tool call; the guard
	// re-prompts ONCE; attempt 2 really calls web_search; the endpoint errors
	// and the tool result carries the backend-error marker; the final answer
	// is the honest fresh failure.
	mock := &staleSearchChatter{script: []*llm.Message{
		{Role: "assistant", Content: "I can't check that right now; the search is still broken."},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call_f3_iii", Type: "function",
			Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "latest pricing"}}}}},
		{Role: "assistant", Content: "The search backend returned an error on this turn, so I could not retrieve the latest pricing just now."},
	}}

	cfg := staleSearchTestCfg(searxng.URL)
	agent := buildStaleSearchAgent(t, db, cfg, mock, convID, true)

	resp, err := agent.Chat(context.Background(), "Search again", func(string) {})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	// The honest fresh failure is delivered — not the stale refusal, not a
	// fabricated answer.
	if !strings.Contains(resp.Content, "search backend returned an error") {
		t.Fatalf("Content = %q — want the honest fresh-failure answer", resp.Content)
	}
	if strings.Contains(resp.Content, "still broken") {
		t.Fatalf("Content = %q — the stale refusal was delivered instead of the fresh failure", resp.Content)
	}
	// Attempt 2 really called web_search against the failing endpoint — the
	// endpoint was reached and errored (the websearch retry decorator issues
	// a few HTTP attempts per logical search for transient 5xx classes, so
	// hits > 0 is the reachability proof). mock.calls == 3 pins the LLM
	// sequence (stale refusal -> web_search tool call -> honest final), which
	// in turn proves exactly ONE tool execution and NO second re-prompt
	// (single-shot guard, and a real web_search ToolResult was recorded this
	// turn).
	if got := atomic.LoadInt32(&searxngHits); got == 0 {
		t.Errorf("SearXNG hits = %d, want > 0 (attempt 2 really called web_search and the offline endpoint errored)", got)
	}
	if mock.calls != 3 {
		t.Errorf("ChatWithTools calls = %d, want 3 (refusal -> web_search -> honest final; no second re-prompt)", mock.calls)
	}
}

// TestHasSearchFailureClaimNoFalsePositive is the full marker verification
// table required by the #424 review: the exact #417 production positive must
// STILL match after the marker narrowing, and the three bot-reported
// false-positive negatives must NOT match.
func TestHasSearchFailureClaimNoFalsePositive(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "417 production positive still matches",
			input: "I'm unable to continue this task without being able to search the web for the latest pricing.",
			want:  true,
		},
		{
			name:  "bot negative: database access",
			input: "without being able to access the database",
			want:  false,
		},
		{
			name:  "bot negative: filesystem access",
			input: "without being able to access the filesystem",
			want:  false,
		},
		{
			name:  "bot negative: reading a file",
			input: "without being able to read that file",
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasSearchFailureClaim(tc.input); got != tc.want {
				t.Errorf("hasSearchFailureClaim(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// countDirectives counts how many entries in a recorded LLM message list carry
// the fresh-search directive, matched by role and exact content.
func countDirectives(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "system" && m.Content == freshSearchDirective {
			n++
		}
	}
	return n
}

// TestStaleSearchDirectiveAppendedOnce is the #424 regression test for the
// redundant fresh-search directive double-append. When BOTH Fix 1
// (retry-intent: the user says "search again" over a trailing failure-claim)
// and Fix 3 (output-gate backstop: attempt 1 refuses without a web_search tool
// call) fire on the same turn, the directive must be appended to turn.Messages
// exactly ONCE — not twice back-to-back. The re-prompt's LLM call (the 2nd
// ChatWithTools invocation) therefore carries the directive a single time.
// Before the guard this test FAILS (directive seen twice); with the guard it
// PASSES (directive seen once). It mirrors the offline e2e harness — no live
// inference, no real SearXNG.
func TestStaleSearchDirectiveAppendedOnce(t *testing.T) {
	t.Parallel()

	// Offline SearXNG endpoint returning one fresh result.
	searxng := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"results":[{"title":"Fresh","url":"https://example.com/fresh","content":"clear skies, 72F"}]}`)
	}))
	defer searxng.Close()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const convID = "f4-conv-directive-once"

	// Seed the PRIOR failed turn so Fix 1's trailing-failure-claim precondition
	// holds (user says "search again" over a recent refusal).
	if err := db.SaveMessage(convID, "parent", "user", "What's the weather?", "", "", ""); err != nil {
		t.Fatalf("seed user msg: %v", err)
	}
	if err := db.SaveMessage(convID, "parent", "assistant", "I couldn't check the weather; the search is still broken.", "", "allow", ""); err != nil {
		t.Fatalf("seed assistant msg: %v", err)
	}

	// Attempt 1 re-emits a stale refusal with no tool call (fires Fix 3's
	// re-prompt); attempt 2 runs web_search; attempt 3 is the fresh final
	// answer. mock.calls == 3 proves BOTH fixes fired and the guard re-prompted.
	mock := &staleSearchChatter{script: []*llm.Message{
		{Role: "assistant", Content: "I can't check that right now; the search is still broken."},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call_f4_1", Type: "function",
			Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "weather today"}}}}},
		{Role: "assistant", Content: "Here's the latest from a fresh search: clear skies, 72°F."},
	}}

	cfg := staleSearchTestCfg(searxng.URL)
	agent := buildStaleSearchAgent(t, db, cfg, mock, convID, true)

	resp, err := agent.Chat(context.Background(), "Search again", func(string) {})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(resp.Content, "fresh search") {
		t.Fatalf("Content = %q — expected the recovered fresh answer", resp.Content)
	}
	if mock.calls != 3 {
		t.Fatalf("ChatWithTools calls = %d, want 3 (Fix 1 + Fix 3 both fired; guard re-prompted)", mock.calls)
	}

	// The re-prompt is the 2nd ChatWithTools call. Count the fresh-search
	// directive in its input message list: exactly ONE (Fix 1's copy), never
	// the back-to-back duplicate that the unguarded Fix 3 path used to add.
	if len(mock.recorded) < 2 {
		t.Fatalf("recorded ChatWithTools inputs = %d, want >= 2", len(mock.recorded))
	}
	if got := countDirectives(mock.recorded[1]); got != 1 {
		t.Errorf("fresh-search directive count in re-prompt input = %d, want 1 (double-append regression)", got)
	}
}
