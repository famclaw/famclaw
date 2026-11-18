package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/famclaw/famclaw/internal/llm"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Project-owned regression tests for the calendar grounding guard.
// These mirror the independent acceptance and transcript fixtures using
// ordinary TestCalendar... names and synthetic data only.

func TestCalendarUnrelatedAndPublicRequestsRemainUnaffected(t *testing.T) {
	for _, input := range []string{
		"What is the weather today?",
		"What will the weather be tomorrow?",
		"Summarize public news this week.",
		"What is on television tonight?",
		"How do I share my calendar? Find public documentation.",
		"Search Google Calendar API authentication documentation.",
		"Tell me a joke.",
		"What do I have to do to learn Go?",
		"Show me all events in Python's asyncio event loop.",
		"What calendars support CalDAV? Find public documentation.",
		"What do I have on disk?",
		"My calendar app keeps crashing. Find troubleshooting documentation.",
	} {
		t.Run(input, func(t *testing.T) {
			const want = "Ordinary response sentinel."
			model := &calendarFixtureChatter{script: []*llm.Message{{Role: "assistant", Content: want}}}
			a, _, events := calendarFixture(t, model, "")
			got, err := a.Chat(context.Background(), input, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.Content != want || atomic.LoadInt32(events) != 0 {
				t.Fatalf("unrelated/public request incorrectly intercepted as a personal schedule: got=%q events=%d", got.Content, atomic.LoadInt32(events))
			}
		})
	}
}

func TestCalendarUnrelatedEllipsisDoesNotInheritCalendar(t *testing.T) {
	model := &calendarFixtureChatter{script: []*llm.Message{
		{Role: "assistant", Content: "A synthetic book summary."},
		{Role: "assistant", Content: "More details about the synthetic book."},
	}}
	a, _, _ := calendarFixture(t, model, "")
	if _, err := a.Chat(context.Background(), "Tell me about this book.", nil); err != nil {
		t.Fatal(err)
	}
	got, err := a.Chat(context.Background(), "What in it?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "More details about the synthetic book." {
		t.Fatalf("unrelated elliptical follow-up became a calendar refusal: %q", got.Content)
	}
}

func TestCalendarDiscoveryNeedsCurrentEvidence(t *testing.T) {
	const invented = "UnverifiedCalendarNameSentinel"
	model := &calendarFixtureChatter{script: []*llm.Message{{Role: "assistant", Content: "I can see " + invented}}}
	a, lists, _ := calendarFixture(t, model, "")
	got, err := a.Chat(context.Background(), "Which calendars can you see now?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(lists) == 0 && strings.Contains(got.Content, invented) {
		t.Fatal("an unsupported calendar-access claim was delivered without a current calendar lookup")
	}
}

func TestCalendarUnsupportedScheduleNeverStreamsOrPersists(t *testing.T) {
	const invented = "InventedAppointmentSentinel"
	model := &calendarFixtureChatter{script: []*llm.Message{{Role: "assistant", Content: invented}}}
	a, _, events := calendarFixture(t, model, "")
	var streamed strings.Builder
	got, err := a.Chat(context.Background(), "Show my calendar events this week.", func(token string) { streamed.WriteString(token) })
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(events) == 0 && (strings.Contains(got.Content, invented) || strings.Contains(streamed.String(), invented)) {
		t.Fatal("unsupported personal schedule escaped through the final or streaming response")
	}
	history, err := a.db.GetConversationHistory(a.convID, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range history {
		if message.Role == "assistant" && strings.Contains(message.Content, invented) {
			t.Fatal("unsupported schedule was persisted as an assistant answer")
		}
	}
}

func TestCalendarCorrectionChainKeepsTheOriginalDomain(t *testing.T) {
	const invented = "InventedCorrectionChainSentinel"
	model := &calendarFixtureChatter{script: []*llm.Message{
		{Role: "assistant", Content: invented}, {Role: "assistant", Content: invented}, {Role: "assistant", Content: invented},
	}}
	a, _, _ := calendarFixture(t, model, "")
	for index, input := range []string{"What is on my calendar this week?", "That is wrong; check again.", "Try again."} {
		got, err := a.Chat(context.Background(), input, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got.Content, invented) {
			t.Fatalf("calendar grounding lost at conversation turn %d: %q", index+1, got.Content)
		}
	}
}

func TestCalendarFamilyFollowupDoesNotAssertStaleAccess(t *testing.T) {
	const invented = "UnverifiedFamilyAccessSentinel"
	model := &calendarFixtureChatter{script: []*llm.Message{
		calendarCall("list-calendars", "inventory", map[string]any{}),
		{Role: "assistant", Content: "Two calendars are currently listed."},
		{Role: "assistant", Content: invented},
	}}
	a, lists, _ := calendarFixture(t, model, "")
	if _, err := a.Chat(context.Background(), "Which calendars can you access?", nil); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(lists) != 1 {
		t.Fatal("initial calendar enumeration not exercised")
	}
	got, err := a.Chat(context.Background(), "How about Family?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(lists) == 1 && strings.Contains(got.Content, invented) {
		t.Fatal("Family follow-up asserted calendar access without refreshing its evidence")
	}
}

func TestCalendarMutationAcknowledgementIsNotAReadRefusal(t *testing.T) {
	const created = "SyntheticCreationAcknowledgedSentinel"
	model := &calendarFixtureChatter{script: []*llm.Message{
		calendarCall("create-event", "create", map[string]any{"calendarId": "cal-a", "summary": "Synthetic appointment"}),
		{Role: "assistant", Content: created},
	}}
	a, _, events := calendarFixture(t, model, "")
	var writes int32
	srv := mcpserver.NewMCPServer("fake-calendar-write", "1")
	srv.AddTool(mcpgo.NewTool("create-event", mcpgo.WithReadOnlyHintAnnotation(false)), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		atomic.AddInt32(&writes, 1)
		return mcpgo.NewToolResultText(`{"status":"created"}`), nil
	})
	if err := a.pool.RegisterInProcessServer("calendar-write-fixture", srv); err != nil {
		t.Fatal(err)
	}
	got, err := a.Chat(context.Background(), "Add a synthetic appointment to my calendar.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&writes) != 1 || atomic.LoadInt32(events) != 0 || !strings.Contains(got.Content, created) {
		t.Fatalf("calendar write was replayed or its success was hidden as an event-read refusal: writes=%d reads=%d answer=%q", atomic.LoadInt32(&writes), atomic.LoadInt32(events), got.Content)
	}
}

// Transcript regression cases.

func TestCalendarObservedEventRequest(t *testing.T) {
	const invented = "InventedObservedEventSentinel"
	model := &calendarFixtureChatter{script: []*llm.Message{{Role: "assistant", Content: invented}}}
	a, _, _ := calendarFixture(t, model, "")
	got, err := a.Chat(context.Background(), "Show me all events from calendar you have access to", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Content, invented) {
		t.Fatal("the observed event-list request bypassed grounding")
	}
}

func TestCalendarObservedContentsFollowup(t *testing.T) {
	const invented = "InventedContentsFollowupSentinel"
	model := &calendarFixtureChatter{script: []*llm.Message{
		calendarCall("list-calendars", "inventory", map[string]any{}),
		{Role: "assistant", Content: "Two calendars are available."},
		{Role: "assistant", Content: invented},
	}}
	a, _, _ := calendarFixture(t, model, "")
	if _, err := a.Chat(context.Background(), "Which calendars can you access?", nil); err != nil {
		t.Fatal(err)
	}
	got, err := a.Chat(context.Background(), "What in it?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Content, invented) {
		t.Fatal("the observed calendar-contents followup bypassed grounding")
	}
}

func TestCalendarInventoryFollowupRemainsInventory(t *testing.T) {
	for _, input := range []string{"How about Family?", "Try again."} {
		t.Run(input, func(t *testing.T) {
			const want = "SyntheticVerifiedCalendarInventory"
			model := &calendarFixtureChatter{script: []*llm.Message{
				calendarCall("list-calendars", "inventory-first", map[string]any{}),
				{Role: "assistant", Content: "Two calendars are available."},
				calendarCall("list-calendars", "inventory-next", map[string]any{}),
				{Role: "assistant", Content: want},
			}}
			a, _, _ := calendarFixture(t, model, "")
			if _, err := a.Chat(context.Background(), "Which calendars can you access?", nil); err != nil {
				t.Fatal(err)
			}
			got, err := a.Chat(context.Background(), input, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.Content != want {
				t.Fatalf("fresh inventory evidence was wrongly treated as missing event data: %q", got.Content)
			}
		})
	}
}

func TestCalendarMixedPrivateRequestNeverUsesPublicRetry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[]}`)
	}))
	t.Cleanup(srv.Close)
	model := &calendarFixtureChatter{script: []*llm.Message{
		{Role: "assistant", Content: "I cannot access your calendar."},
		calendarCall("builtin__web_search", "public", map[string]any{"query": "synthetic-private-mixed-canary"}),
		{Role: "assistant", Content: "No public results."},
	}}
	a, _, _ := calendarFixture(t, model, srv.URL)
	if _, err := a.Chat(context.Background(), "Check my private calendar this week and find public documentation on sharing.", nil); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("public-documentation wording disabled personal-calendar retry isolation")
	}
}

func TestCalendarPublicEventAPIDocumentation(t *testing.T) {
	const want = "SyntheticPublicAPIDocumentation"
	model := &calendarFixtureChatter{script: []*llm.Message{{Role: "assistant", Content: want}}}
	a, _, _ := calendarFixture(t, model, "")
	got, err := a.Chat(context.Background(), "Find Google Calendar API documentation for events.list.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != want {
		t.Fatalf("public events API research became a personal-calendar refusal: %q", got.Content)
	}
}

func TestCalendarContentsCorrectionRetainsEventRead(t *testing.T) {
	const want = "Synthetic verified appointment."
	model := &calendarFixtureChatter{script: []*llm.Message{
		calendarCall("list-calendars", "inventory", map[string]any{}),
		{Role: "assistant", Content: "Two calendars are available."},
		{Role: "assistant", Content: "InventedBeforeContentsCorrection"},
		calendarCall("list-events", "events", map[string]any{"calendarId": "cal-a"}),
		{Role: "assistant", Content: want},
	}}
	a, _, _ := calendarFixture(t, model, "")
	if _, err := a.Chat(context.Background(), "Which calendars can you access?", nil); err != nil {
		t.Fatal(err)
	}
	second, err := a.Chat(context.Background(), "What in it?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(second.Content, "InventedBeforeContentsCorrection") {
		t.Fatal("unsupported contents claim escaped")
	}
	got, err := a.Chat(context.Background(), "Try again.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != want {
		t.Fatalf("contents correction lost event-read domain despite fresh event evidence: %q", got.Content)
	}
}

func TestCalendarExplicitRetryRequestEstablishesDomain(t *testing.T) {
	const invented = "InventedExplicitRetryCalendarSentinel"
	model := &calendarFixtureChatter{script: []*llm.Message{{Role: "assistant", Content: invented}, {Role: "assistant", Content: invented}}}
	a, _, _ := calendarFixture(t, model, "")
	for index, input := range []string{"Check again: what is on my calendar?", "Try again."} {
		got, err := a.Chat(context.Background(), input, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got.Content, invented) {
			t.Fatalf("explicit calendar retry lost its domain at turn %d", index+1)
		}
	}
}
