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
		name        string
		state       store.ResearchStatusState
		resultText  string
		timeoutSec  int
		wantSubstr  string
	}{
		{name: "completed", state: store.ResearchStatusCompleted, resultText: "odyssey not playing", timeoutSec: 300, wantSubstr: "🔬 Research task agent-1 completed:\nodyssey not playing"},
		{name: "failed", state: store.ResearchStatusFailed, resultText: "HTTP 404", timeoutSec: 300, wantSubstr: "❌ Research task agent-1 failed: HTTP 404"},
		{name: "timed_out", state: store.ResearchStatusTimedOut, resultText: "context deadline", timeoutSec: 300, wantSubstr: "⏰ Research task agent-1 timed out after 300 seconds."},
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

	tests := []struct {
		name     string
		result   subagent.Result
		ctx      context.Context
		wantState store.ResearchStatusState
	}{
		{name: "success", result: subagent.Result{Output: "ok"}, ctx: context.Background(), wantState: store.ResearchStatusCompleted},
		{name: "error not timeout", result: subagent.Result{Error: fmt.Errorf("boom")}, ctx: context.Background(), wantState: store.ResearchStatusFailed},
		{name: "timeout takes precedence", result: subagent.Result{Error: fmt.Errorf("deadline exceeded")}, ctx: deadlineCtx, wantState: store.ResearchStatusTimedOut},
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
