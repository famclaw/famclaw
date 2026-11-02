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
	script []*llm.Message
	calls  int
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

func (m *staleSearchChatter) ChatWithTools(_ context.Context, _ []llm.Message, _ float64, _ int, _ []llm.ToolDef) (*llm.Message, error) {
	idx := m.calls
	m.calls++
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
//   1. A prior turn records a search failure ("I couldn't check ... search is
//      still broken").
//   2. The user explicitly retries ("Search again").
//   3. The model FIRST emits a stale refusal without calling web_search.
//   4. The fresh-search guard re-prompts; the model runs a real web_search
//      against the httptest SearXNG endpoint and returns a fresh answer.
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
