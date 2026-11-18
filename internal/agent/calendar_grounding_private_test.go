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
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/websearch"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

type calendarFixtureChatter struct {
	script   []*llm.Message
	calls    int
	messages [][]llm.Message
}

func (m *calendarFixtureChatter) Chat(context.Context, []llm.Message, float64, int, func(string)) (string, error) {
	return "", errors.New("unexpected Chat")
}
func (m *calendarFixtureChatter) ChatSync(context.Context, []llm.Message, float64, int) (string, error) {
	return "", errors.New("unexpected ChatSync")
}
func (m *calendarFixtureChatter) ChatMessage(context.Context, []llm.Message, float64, int) (*llm.Message, error) {
	return nil, errors.New("unexpected ChatMessage")
}
func (m *calendarFixtureChatter) ChatWithTools(_ context.Context, msgs []llm.Message, _ float64, _ int, _ []llm.ToolDef) (*llm.Message, error) {
	m.messages = append(m.messages, append([]llm.Message(nil), msgs...))
	i := m.calls
	m.calls++
	if i >= len(m.script) {
		return nil, errors.New("script exhausted")
	}
	return m.script[i], nil
}
func calendarCall(name, id string, args map[string]any) *llm.Message {
	return &llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Type: "function", Function: llm.ToolCallFunction{Name: name, Arguments: args}}}}
}
func calendarFixture(t *testing.T, m *calendarFixtureChatter, searchURL string) (*Agent, *int32, *int32) {
	t.Helper()
	var lists, events int32
	srv := mcpserver.NewMCPServer("fake-calendar", "1")
	srv.AddTool(mcpgo.NewTool("list-calendars", mcpgo.WithReadOnlyHintAnnotation(true)), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		atomic.AddInt32(&lists, 1)
		return mcpgo.NewToolResultText(`{"calendars":[{"id":"cal-a"},{"id":"cal-b"}],"totalCount":2}`), nil
	})
	srv.AddTool(mcpgo.NewTool("list-events", mcpgo.WithReadOnlyHintAnnotation(true)), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		atomic.AddInt32(&events, 1)
		return mcpgo.NewToolResultText(`{"events":[{"summary":"Synthetic verified appointment"}],"totalCount":1}`), nil
	})
	pool := mcp.NewPool("", false, false)
	if err := pool.RegisterInProcessServer("calendar-fixture", srv); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.StopAll)
	db, err := store.Open(filepath.Join(t.TempDir(), "calendar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LLM: config.LLMConfig{MaxResponseTokens: 200, MaxContextTokens: 8000}, Users: []config.UserConfig{{Name: "parent", Role: "parent"}}}
	var builtins []agentcore.Tool
	if searchURL != "" {
		cfg.Tools.WebFetch = config.WebFetchConfig{Enabled: true, URLAllowlist: []string{"127.0.0.1"}}
		cfg.Tools.WebSearch = config.WebSearchConfig{Enabled: true, Endpoint: searchURL, TimeoutSec: 5, MaxResults: 5}
		builtins = []agentcore.Tool{websearch.Tool([]string{"parent"})}
	}
	a, err := NewAgent(&cfg.Users[0], cfg, m, ev, classifier.New(), db, AgentDeps{Pool: pool, BuiltinTools: builtins, MsgContext: gateway.MsgContext{ConvID: "synthetic-calendar-conversation"}})
	if err != nil {
		t.Fatal(err)
	}
	return a, &lists, &events
}

// The fake calendar listing establishes available calendars, not their events.
// The scripted model then reuses a fabricated synthetic answer after correction.
func TestCalendarGroundingRejectsUnlookedUpRepeatedEvents(t *testing.T) {
	stale := "Your calendar has a synthetic planning block on Tuesday."
	model := &calendarFixtureChatter{script: []*llm.Message{calendarCall("list-calendars", "enum", map[string]any{}), {Role: "assistant", Content: "Two calendars are available."}, {Role: "assistant", Content: stale}, {Role: "assistant", Content: stale}}}
	a, lists, events := calendarFixture(t, model, "")
	first, err := a.Chat(context.Background(), "Which calendars can you access?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(lists) != 1 || !strings.Contains(first.Content, "Two calendars") {
		t.Fatalf("enumeration fixture not established: lists=%d", atomic.LoadInt32(lists))
	}
	answer, err := a.Chat(context.Background(), "What is on my calendar this week?", nil)
	if err != nil {
		t.Fatal(err)
	}
	corrected, err := a.Chat(context.Background(), "That is wrong; check again.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(events) == 0 && (strings.Contains(answer.Content, "synthetic planning block") || strings.Contains(corrected.Content, "synthetic planning block")) {
		t.Errorf("ungrounded event assertion delivered after enumeration and correction: calendar_lists=%d event_lookups=%d repeated=%t", atomic.LoadInt32(lists), atomic.LoadInt32(events), answer.Content == corrected.Content)
	}
}

func TestCalendarGroundingFakeMCPPositiveControl(t *testing.T) {
	model := &calendarFixtureChatter{script: []*llm.Message{calendarCall("list-events", "events", map[string]any{"calendarId": "cal-a"}), {Role: "assistant", Content: "Synthetic verified appointment."}}}
	a, _, events := calendarFixture(t, model, "")
	answer, err := a.Chat(context.Background(), "What is on my calendar?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(events) != 1 || !strings.Contains(answer.Content, "Synthetic verified appointment") {
		t.Fatalf("fake MCP event control not exercised: event_lookups=%d", atomic.LoadInt32(events))
	}
}

// A private-calendar denial can satisfy the general web-search retry predicate.
// This fake model follows the injected instruction; the only network endpoint
// is an httptest listener inside an unshared network namespace.
func TestCalendarRetryNeverRoutesPrivateIntentToWebSearch(t *testing.T) {
	var searchCalls int32
	var sawCanary atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&searchCalls, 1)
		if strings.Contains(r.URL.Query().Get("q"), "synthetic-private-calendar-canary") {
			sawCanary.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[]}`)
	}))
	t.Cleanup(srv.Close)
	model := &calendarFixtureChatter{script: []*llm.Message{{Role: "assistant", Content: "I cannot access your calendar."}, calendarCall("builtin__web_search", "web", map[string]any{"query": "synthetic-private-calendar-canary"}), {Role: "assistant", Content: "No public results."}}}
	a, _, events := calendarFixture(t, model, srv.URL)
	_, err := a.Chat(context.Background(), "Check my private calendar.", nil)
	if err != nil {
		t.Fatal(err)
	}
	injected := false
	for _, batch := range model.messages {
		for _, msg := range batch {
			if msg.Role == "system" && strings.Contains(msg.Content, freshSearchDirective) {
				injected = true
			}
		}
	}
	if atomic.LoadInt32(&searchCalls) > 0 || sawCanary.Load() {
		t.Errorf("calendar request reached fake web-search endpoint: web_calls=%d synthetic_canary_received=%t directive_injected=%t calendar_event_calls=%d", atomic.LoadInt32(&searchCalls), sawCanary.Load(), injected, atomic.LoadInt32(events))
	}
}
