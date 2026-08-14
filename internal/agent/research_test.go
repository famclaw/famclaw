package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/subagent"
)

// fakeClock is an injectable clock for deterministic research-status
// timestamps, including the running→terminal transition.
type fakeClock struct {
	t time.Time
}

func (c *fakeClock) now() time.Time { return c.t }

// errSender is a gateway.Sender stub that always fails with a configurable
// error — modeling the live 404 "Unknown Channel" delivery failure.
type errSender struct{ err error }

func (e *errSender) Send(ctx context.Context, chatID, text string) error { return e.err }

// setupResearchAgent builds an agent wired with a DB, a sender, a fixed
// conversation id, and a configurable clock. Reuses setupAgent for the
// evaluator/classifier/config plumbing.
func setupResearchAgent(t *testing.T, sender gateway.Sender) *Agent {
	t.Helper()
	a := setupAgent(t, "")
	a.senderRegistry = map[string]gateway.Sender{"telegram": sender}
	a.convID = "research-test-conv"
	a.msgContext = gateway.MsgContext{
		Gateway:    "telegram",
		ExternalID: "chat-1",
	}
	return a
}

func TestBuildResearchDeliverable(t *testing.T) {
	tests := []struct {
		name       string
		state      store.ResearchStatusState
		resultText string
		timeoutSec int
		wantSubstr string
	}{
		{name: "completed", state: store.ResearchStatusCompleted, resultText: "odyssey not playing", timeoutSec: 300, wantSubstr: "🔬 Research task agent-1 completed:\nodyssey not playing"},
		{name: "failed", state: store.ResearchStatusFailed, resultText: "HTTP 404", timeoutSec: 300, wantSubstr: "❌ Research task agent-1 failed: HTTP 404"},
		{name: "timed_out", state: store.ResearchStatusTimedOut, resultText: "context deadline", timeoutSec: 300, wantSubstr: "timed out after 300 seconds"},
		{name: "unknown state falls back", state: "weird", resultText: "x", timeoutSec: 1, wantSubstr: "📋 Research task agent-1: x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildResearchDeliverable("agent-1", tc.state, tc.resultText, tc.timeoutSec)
			if !strings.Contains(got, tc.wantSubstr) {
				t.Errorf("got %q, want substring %q", got, tc.wantSubstr)
			}
		})
	}
}

func TestClassifySubagentResult(t *testing.T) {
	deadline := 1 * time.Millisecond
	deadlineCtx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	time.Sleep(5 * time.Millisecond)

	cancelCtx, cancelCtxCancel := context.WithCancel(context.Background())
	cancelCtxCancel()

	tests := []struct {
		name      string
		result    subagent.Result
		ctx       context.Context
		wantState store.ResearchStatusState
	}{
		{name: "success", result: subagent.Result{Output: "ok"}, ctx: context.Background(), wantState: store.ResearchStatusCompleted},
		{name: "error not timeout", result: subagent.Result{Error: fmt.Errorf("boom")}, ctx: context.Background(), wantState: store.ResearchStatusFailed},
		{name: "timeout takes precedence", result: subagent.Result{Error: fmt.Errorf("deadline exceeded")}, ctx: deadlineCtx, wantState: store.ResearchStatusTimedOut},
		{name: "cancelled context is failed not timeout", result: subagent.Result{Error: fmt.Errorf("context canceled")}, ctx: cancelCtx, wantState: store.ResearchStatusFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state, _ := classifySubagentResult(tc.result, tc.ctx)
			if state != tc.wantState {
				t.Errorf("state = %q, want %q", state, tc.wantState)
			}
		})
	}
}

// TestFinalizeResearch_DeliveryFailure is the core regression: when the
// gateway returns 404 (Unknown Channel), the failure must NOT be swallowed —
// it is persisted as a failed, not-delivered status AND surfaced into the
// conversation history so the user stops seeing "still working".
func TestFinalizeResearch_DeliveryFailure(t *testing.T) {
	sender := &errSender{err: fmt.Errorf("HTTP 404 Not Found, {\"message\":\"Unknown Channel\",\"code\":10003}")}
	a := setupResearchAgent(t, sender)
	clock := &fakeClock{t: time.Date(2026, 7, 23, 18, 18, 13, 0, time.UTC)}
	a.nowFn = clock.now

	const deliverableWant = "❌ Research task agent-1 failed: boom"
	a.finalizeResearch(context.Background(), "agent-1", store.ResearchStatusFailed, "boom", 300, "find odyssey", a.msgContext)

	s, err := a.db.GetResearchStatus(context.Background(), a.user.Name, "agent-1")
	if err != nil {
		t.Fatalf("GetResearchStatus: %v", err)
	}
	if s == nil {
		t.Fatal("expected a status record; got nil")
	}
	if s.Status != store.ResearchStatusFailed {
		t.Errorf("status = %q, want %q", s.Status, store.ResearchStatusFailed)
	}
	if s.Delivered {
		t.Error("expected Delivered=false after a 404 send failure")
	}
	if !strings.Contains(s.DeliveryErr, "404") {
		t.Errorf("DeliveryErr = %q, want it to contain %q", s.DeliveryErr, "404")
	}
	if s.Deliverable != deliverableWant {
		t.Errorf("Deliverable = %q, want %q", s.Deliverable, deliverableWant)
	}
	if !s.EndedAt.Equal(clock.t) {
		t.Errorf("EndedAt = %v, want %v", s.EndedAt, clock.t)
	}
	if s.Prompt != "find odyssey" {
		t.Errorf("Prompt = %q, want %q", s.Prompt, "find odyssey")
	}

	// The result must have been surfaced into the conversation history even
	// though channel delivery failed.
	hist, err := a.db.GetConversationHistory(a.convID, 20)
	if err != nil {
		t.Fatalf("GetConversationHistory: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("expected 1 conversation message, got %d", len(hist))
	}
	if hist[0].Role != "assistant" {
		t.Errorf("role = %q, want %q", hist[0].Role, "assistant")
	}
	if !strings.Contains(hist[0].Content, deliverableWant) {
		t.Errorf("history content = %q, want substring %q", hist[0].Content, deliverableWant)
	}
}

// TestFinalizeResearch_DeliverySuccess asserts the happy path persists a
// completed, delivered status.
func TestFinalizeResearch_DeliverySuccess(t *testing.T) {
	sink := &mockSender{calls: make(chan *senderCall, 1)}
	a := setupResearchAgent(t, sink)
	a.nowFn = func() time.Time { return time.Date(2026, 7, 23, 18, 30, 0, 0, time.UTC) }

	a.finalizeResearch(context.Background(), "agent-1", store.ResearchStatusCompleted, "the odyssey is not in theaters", 300, "research odyssey", a.msgContext)

	s, err := a.db.GetResearchStatus(context.Background(), a.user.Name, "agent-1")
	if err != nil || s == nil {
		t.Fatalf("expected a status record, err=%v s=%v", err, s)
	}
	if s.Status != store.ResearchStatusCompleted {
		t.Errorf("status = %q, want %q", s.Status, store.ResearchStatusCompleted)
	}
	if !s.Delivered {
		t.Error("expected Delivered=true on a successful send")
	}
	if s.DeliveryErr != "" {
		t.Errorf("DeliveryErr = %q, want empty", s.DeliveryErr)
	}

	// The result was delivered to the originating conversation via the sender.
	select {
	case call := <-sink.calls:
		if call.chatID != "chat-1" {
			t.Errorf("chatID = %q, want %q", call.chatID, "chat-1")
		}
		if !strings.Contains(call.text, "🔬 Research task agent-1 completed") {
			t.Errorf("delivered text = %q, want completed marker", call.text)
		}
	default:
		t.Error("expected a sender call, got none")
	}
}

// TestFinalizeResearch_TimedOut verifies the timeout outcome text and that
// the failure is preserved in the status record when delivery also fails.
func TestFinalizeResearch_TimedOut(t *testing.T) {
	sender := &errSender{err: fmt.Errorf("no sender")}
	a := setupResearchAgent(t, sender)
	a.finalizeResearch(context.Background(), "agent-1", store.ResearchStatusTimedOut, "context deadline exceeded", 300, "deep research", a.msgContext)

	out, err := a.handleResearchStatus(context.Background(), map[string]any{"agent_id": "agent-1"})
	if err != nil {
		t.Fatalf("handleResearchStatus: %v", err)
	}
	if !strings.Contains(out, "timed_out") {
		t.Errorf("status output = %q, want it to mention timed_out", out)
	}
	if !strings.Contains(out, "timed out after 300 seconds") {
		t.Errorf("status output = %q, want the timeout message", out)
	}
	if !strings.Contains(out, "NOT delivered") {
		t.Errorf("status output = %q, want NOT delivered marker", out)
	}
}

// TestResearchStatus_RunningThenTerminal verifies that a "running" record
// survives the running→terminal transition: started_at is preserved and
// ended_at is set to the terminal time.
func TestResearchStatus_RunningThenTerminal(t *testing.T) {
	a := setupResearchAgent(t, &mockSender{calls: make(chan *senderCall, 1)})
	clock := &fakeClock{t: time.Date(2026, 7, 23, 18, 17, 39, 0, time.UTC)}
	a.nowFn = clock.now

	a.persistResearchStart("agent-1", "find odyssey", 300, a.msgContext)
	clock.t = time.Date(2026, 7, 23, 18, 18, 13, 0, time.UTC)
	a.finalizeResearch(context.Background(), "agent-1", store.ResearchStatusFailed, "boom", 300, "find odyssey", a.msgContext)

	s, err := a.db.GetResearchStatus(context.Background(), a.user.Name, "agent-1")
	if err != nil || s == nil {
		t.Fatalf("expected a status record, err=%v s=%v", err, s)
	}
	if !s.StartedAt.Equal(time.Date(2026, 7, 23, 18, 17, 39, 0, time.UTC)) {
		t.Errorf("StartedAt = %v, want the original start time preserved", s.StartedAt)
	}
	if !s.EndedAt.Equal(time.Date(2026, 7, 23, 18, 18, 13, 0, time.UTC)) {
		t.Errorf("EndedAt = %v, want the terminal time", s.EndedAt)
	}
}

// TestFinalizeResearch_UsesMsgCtxGateway verifies that the research result
// saved to the conversation history carries msgCtx.Gateway (the gateway the
// request actually arrived on), NOT a.auditGateway (the agent construction-time
// value). This is the core guarantee of per-message gateway recording: a
// research task spawned from a Discord message must be attributed to Discord
// even if the agent was constructed with a different default gateway.
func TestFinalizeResearch_UsesMsgCtxGateway(t *testing.T) {
	sink := &mockSender{calls: make(chan *senderCall, 1)}
	a := setupResearchAgent(t, sink)
	// Agent constructed with telegram as its default gateway, but the
	// research request actually arrives on Discord.
	a.auditGateway = "telegram"
	a.senderRegistry["discord"] = sink

	discordCtx := gateway.MsgContext{
		Gateway:    "discord",
		ExternalID: "discord-chat-1",
	}
	a.finalizeResearch(context.Background(), "agent-1", store.ResearchStatusCompleted, "the answer", 300, "research", discordCtx)

	// The message saved to the conversation history must carry the
	// msgCtx gateway (discord), not the agent's construction-time gateway
	// (telegram).
	hist, err := a.db.GetConversationHistory(a.convID, 20)
	if err != nil {
		t.Fatalf("GetConversationHistory: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("expected 1 conversation message, got %d", len(hist))
	}
	if hist[0].Gateway != "discord" {
		t.Errorf("saved message gateway = %q, want %q (msgCtx gateway, not agent gateway %q)",
			hist[0].Gateway, "discord", a.auditGateway)
	}

	// The research status record must also capture the msgCtx gateway.
	s, err := a.db.GetResearchStatus(context.Background(), a.user.Name, "agent-1")
	if err != nil || s == nil {
		t.Fatalf("expected a status record, err=%v s=%v", err, s)
	}
	if s.Gateway != "discord" {
		t.Errorf("research status gateway = %q, want %q", s.Gateway, "discord")
	}
}

// TestFinalizeResearch_ZeroValueMsgContextSavesUnknown verifies that when
// finalizeResearch is called with a zero-valued MsgContext, the saved
// gateway is "unknown" — never an empty string. This prevents
// MostRecentGatewayForUser from returning "", which would cause
// cross-chat delivery to misroute.
func TestFinalizeResearch_ZeroValueMsgContextSavesUnknown(t *testing.T) {
	sink := &mockSender{calls: make(chan *senderCall, 1)}
	a := setupResearchAgent(t, sink)

	zeroCtx := gateway.MsgContext{}
	a.finalizeResearch(context.Background(), "agent-1", store.ResearchStatusCompleted, "the answer", 300, "research", zeroCtx)

	hist, err := a.db.GetConversationHistory(a.convID, 20)
	if err != nil {
		t.Fatalf("GetConversationHistory: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("expected 1 conversation message, got %d", len(hist))
	}
	if hist[0].Gateway != "unknown" {
		t.Errorf("zero-value msgCtx gateway = %q, want %q", hist[0].Gateway, "unknown")
	}

	s, err := a.db.GetResearchStatus(context.Background(), a.user.Name, "agent-1")
	if err != nil || s == nil {
		t.Fatalf("expected a status record, err=%v s=%v", err, s)
	}
	if s.Gateway != "unknown" {
		t.Errorf("research status gateway = %q, want %q", s.Gateway, "unknown")
	}
}

// TestPersistResearchStart_Running verifies the initial running record.
func TestPersistResearchStart_Running(t *testing.T) {
	a := setupResearchAgent(t, &errSender{err: fmt.Errorf("nope")})
	a.nowFn = func() time.Time { return time.Date(2026, 7, 23, 18, 17, 39, 0, time.UTC) }

	a.persistResearchStart("agent-1", "find odyssey", 300, a.msgContext)

	s, err := a.db.GetResearchStatus(context.Background(), a.user.Name, "agent-1")
	if err != nil || s == nil {
		t.Fatalf("expected a running status, err=%v s=%v", err, s)
	}
	if s.Status != store.ResearchStatusRunning {
		t.Errorf("status = %q, want %q", s.Status, store.ResearchStatusRunning)
	}
	if s.EndedAt != nil {
		t.Errorf("EndedAt = %v, want nil while running", s.EndedAt)
	}
	if s.Gateway != "telegram" || s.ChatID != "chat-1" {
		t.Errorf("gateway=%q chatID=%q, want telegram / chat-1", s.Gateway, s.ChatID)
	}
}

// TestHandleResearchStatus_ByAgentID checks the tool returns a single task.
func TestHandleResearchStatus_ByAgentID(t *testing.T) {
	a := setupResearchAgent(t, &mockSender{calls: make(chan *senderCall, 1)})
	a.finalizeResearch(context.Background(), "agent-1", store.ResearchStatusCompleted, "the odyssey is not playing any theaters this Sunday", 300, "research odyssey", a.msgContext)

	out, err := a.handleResearchStatus(context.Background(), map[string]any{"agent_id": "agent-1"})
	if err != nil {
		t.Fatalf("handleResearchStatus: %v", err)
	}
	for _, want := range []string{"agent-1", "completed", "delivered ✅", "the odyssey is not playing"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want substring %q", out, want)
		}
	}
}

// TestHandleResearchStatus_List checks the no-arg list path.
func TestHandleResearchStatus_List(t *testing.T) {
	a := setupResearchAgent(t, &mockSender{calls: make(chan *senderCall, 1)})
	a.finalizeResearch(context.Background(), "agent-1", store.ResearchStatusCompleted, "result one", 300, "p1", a.msgContext)

	out, err := a.handleResearchStatus(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("handleResearchStatus: %v", err)
	}
	if !strings.Contains(out, "agent-1") || !strings.Contains(out, "result one") {
		t.Errorf("list output = %q, want agent-1 and result one", out)
	}
}

// TestHandleResearchStatus_Missing asserts a clean message for unknown tasks.
func TestHandleResearchStatus_Missing(t *testing.T) {
	a := setupResearchAgent(t, &mockSender{calls: make(chan *senderCall, 1)})
	out, err := a.handleResearchStatus(context.Background(), map[string]any{"agent_id": "agent-999"})
	if err != nil {
		t.Fatalf("handleResearchStatus: %v", err)
	}
	if !strings.Contains(out, "No research task") {
		t.Errorf("output = %q, want 'No research task'", out)
	}
}

// TestResearchStatusToolDef confirms the tool is registered with the right
// name and an agent_id parameter.
func TestResearchStatusToolDef(t *testing.T) {
	tool := ResearchStatusTool()
	if tool.Name != "builtin__research_status" {
		t.Errorf("name = %q, want builtin__research_status", tool.Name)
	}
	props, _ := tool.InputSchema["properties"].(map[string]any)
	if _, ok := props["agent_id"]; !ok {
		t.Error("expected agent_id property in input schema")
	}
}

// TestResolveSubagentOutcome_FastFailureNotTimeout is the core regression for
// the phantom-timeout bug. When the subagent's context is cancelled by the
// parent chat turn ending (context.Canceled, NOT a 300s deadline) before a
// result arrives, the outcome must be Failed -- never TimedOut. The old
// inline code in handleSpawnAgent unconditionally returned ResearchStatusTimedOut
// on the ctx.Done() branch regardless of subCtx.Err(), so a one-second
// failure became "timed out after 300 seconds."
func TestResolveSubagentOutcome_FastFailureNotTimeout(t *testing.T) {
	tests := []struct {
		name       string
		resultCh   chan subagent.Result
		subCtx     context.Context
		wantState  store.ResearchStatusState
		wantSubstr string
	}{
		{
			name: "cancelled context no result is failed not timeout",
			// Parent chat turn cancelled the context -- NOT a deadline.
			// No result arrived on the channel before context was done.
			resultCh: func() chan subagent.Result {
				return make(chan subagent.Result, 1) // never sent
			}(),
			subCtx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			wantState:  store.ResearchStatusFailed,
			wantSubstr: "context canceled",
		},
		{
			name: "deadline exceeded is still timeout",
			resultCh: func() chan subagent.Result {
				return make(chan subagent.Result, 1) // never sent
			}(),
			subCtx: func() context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
				time.Sleep(5 * time.Millisecond)
				cancel()
				return ctx
			}(),
			wantState:  store.ResearchStatusTimedOut,
			wantSubstr: "deadline",
		},
		{
			name: "result with error and live context is failed",
			resultCh: func() chan subagent.Result {
				ch := make(chan subagent.Result, 1)
				ch <- subagent.Result{Error: fmt.Errorf("subagent LLM call: connection refused")}
				return ch
			}(),
			subCtx:     context.Background(),
			wantState:  store.ResearchStatusFailed,
			wantSubstr: "connection refused",
		},
		{
			name: "result with success is completed",
			resultCh: func() chan subagent.Result {
				ch := make(chan subagent.Result, 1)
				ch <- subagent.Result{Output: "HVAC companies found"}
				return ch
			}(),
			subCtx:     context.Background(),
			wantState:  store.ResearchStatusCompleted,
			wantSubstr: "HVAC companies found",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state, resultText := resolveSubagentOutcome(tc.resultCh, tc.subCtx)
			if state != tc.wantState {
				t.Errorf("state = %q, want %q", state, tc.wantState)
			}
			if !strings.Contains(resultText, tc.wantSubstr) {
				t.Errorf("resultText = %q, want substring %q", resultText, tc.wantSubstr)
			}
			// CRITICAL REGRESSION ASSERTION: a Failed state must NEVER be
			// rendered as the fabricated 300-second timeout deliverable.
			if state == store.ResearchStatusFailed {
				deliverable := buildResearchDeliverable("agent-1", state, resultText, 300)
				if strings.Contains(deliverable, "timed out after 300 seconds") {
					t.Errorf("Failed deliverable = %q, must NOT contain 'timed out after 300 seconds' -- a fast failure must not be fabricating a 300s timeout", deliverable)
				}
			}
			// A genuine TimedOut state SHOULD carry the real deadline error.
			if state == store.ResearchStatusTimedOut {
				if !strings.Contains(resultText, "exceeded") {
					t.Errorf("TimedOut resultText = %q, want it to contain the actual deadline error", resultText)
				}
			}
		})
	}
}

// TestShutdownCancelledSubagent_RecordedAsFailed is the regression test for
// the captain's feedback: when the server-lifetime context is cancelled at
// shutdown, the subagent must be recorded as Failed with a truthful reason
// (the actual cancellation error), NOT as a 300-second timeout. This covers
// the full flow: resolveSubagentOutcome classifies the outcome, then
// finalizeResearch persists it to the DB.
func TestShutdownCancelledSubagent_RecordedAsFailed(t *testing.T) {
	sender := &errSender{err: fmt.Errorf("delivery will fail")}
	a := setupResearchAgent(t, sender)
	a.nowFn = func() time.Time { return time.Date(2026, 7, 23, 18, 30, 0, 0, time.UTC) }

	// Simulate the server-lifetime context that is cancelled at shutdown.
	lifetimeCtx, lifetimeCancel := context.WithCancel(context.Background())
	// Derive the subagent's timeout context from the lifetime context,
	// exactly as handleSpawnAgent does after the fix.
	subCtx, subCancel := context.WithTimeout(lifetimeCtx, 300*time.Second)
	defer subCancel()

	// Simulate graceful shutdown: cancel the lifetime context. This is
	// context.Canceled (NOT DeadlineExceeded), because the deadline
	// (300s) has not elapsed.
	lifetimeCancel()
	// Give the child context time to observe the cancellation.
	time.Sleep(5 * time.Millisecond)

	// No result arrived — the context was done first (shutdown).
	resultCh := make(chan subagent.Result, 1)
	state, resultText := resolveSubagentOutcome(resultCh, subCtx)

	if state != store.ResearchStatusFailed {
		t.Fatalf("state = %q, want %q — a shutdown cancellation must not be reported as a 300s timeout", state, store.ResearchStatusFailed)
	}
	if !strings.Contains(resultText, "context canceled") {
		t.Errorf("resultText = %q, want it to contain the truthful cancellation reason 'context canceled'", resultText)
	}
	if strings.Contains(resultText, "deadline") {
		t.Errorf("resultText = %q, must NOT claim a deadline — the shutdown happened in ~0s, not 300s", resultText)
	}

	// Now persist it through finalizeResearch (using a bounded finalization
	// context, as handleSpawnAgent does).
	finalCtx, finalCancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer finalCancel()
	a.finalizeResearch(finalCtx, "agent-shutdown", state, resultText, 300, "find hvac", a.msgContext)

	// Verify the DB record is truthful.
	s, err := a.db.GetResearchStatus(context.Background(), a.user.Name, "agent-shutdown")
	if err != nil {
		t.Fatalf("GetResearchStatus: %v", err)
	}
	if s == nil {
		t.Fatal("expected a status record; got nil")
	}
	if s.Status != store.ResearchStatusFailed {
		t.Errorf("DB status = %q, want %q", s.Status, store.ResearchStatusFailed)
	}
	if strings.Contains(s.Deliverable, "timed out after 300 seconds") {
		t.Errorf("deliverable = %q, must NOT contain 'timed out after 300 seconds'", s.Deliverable)
	}
	if !strings.Contains(s.Deliverable, "context canceled") {
		t.Errorf("deliverable = %q, should contain the truthful reason", s.Deliverable)
	}
	if !strings.Contains(s.Deliverable, "agent-shutdown") {
		t.Errorf("deliverable = %q, should mention the agent ID", s.Deliverable)
	}
}

// TestResolveSubagentOutcome_LifetimeContextShutdown verifies that a subagent
// whose parent lifetime context was cancelled (shutdown) — with the deadline
// still far in the future — is classified as Failed, not TimedOut. This is a
// table-driven case complementing the full flow test above.
func TestResolveSubagentOutcome_LifetimeContextShutdown(t *testing.T) {
	lifetimeCtx, lifetimeCancel := context.WithCancel(context.Background())
	subCtx, subCancel := context.WithTimeout(lifetimeCtx, 300*time.Second)
	defer subCancel()

	// Cancel the lifetime context to simulate shutdown.
	lifetimeCancel()
	time.Sleep(5 * time.Millisecond)

	// No result arrives; the subCtx is done due to parent cancellation.
	resultCh := make(chan subagent.Result, 1)
	state, resultText := resolveSubagentOutcome(resultCh, subCtx)

	// Must be Failed, never TimedOut.
	if state != store.ResearchStatusFailed {
		t.Errorf("state = %q, want %q (shutdown cancellation must not be a timeout)", state, store.ResearchStatusFailed)
	}
	// Must carry the truthful reason.
	if !strings.Contains(resultText, "context canceled") {
		t.Errorf("resultText = %q, want 'context canceled'", resultText)
	}
	// Must NOT claim a deadline.
	if strings.Contains(resultText, "deadline") {
		t.Errorf("resultText = %q, must not claim deadline exceeded", resultText)
	}
}
