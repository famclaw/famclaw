package trello

import (
	"context"
	"fmt"
	"strings"
	"testing"

	gclient "github.com/mark3labs/mcp-go/client"
	gmcp "github.com/mark3labs/mcp-go/mcp"
)

// mockClient is a Client double for handler tests.
type mockClient struct {
	addCardFn      func(ctx context.Context, listID, title, desc string) (Card, error)
	listCardsFn    func(ctx context.Context, listID string) ([]Card, error)
	completeCardFn func(ctx context.Context, cardID string) (Card, error)

	addCardArgs      []struct{ listID, title, desc string }
	listCardsArgs    []struct{ listID string }
	completeCardArgs []struct{ cardID string }
}

func (m *mockClient) AddCard(ctx context.Context, listID, title, desc string) (Card, error) {
	m.addCardArgs = append(m.addCardArgs, struct{ listID, title, desc string }{listID, title, desc})
	if m.addCardFn != nil {
		return m.addCardFn(ctx, listID, title, desc)
	}
	return Card{ID: "c1", Name: title, ShortLink: "c1", IDList: listID, ShortURL: "u"}, nil
}

func (m *mockClient) ListCards(ctx context.Context, listID string) ([]Card, error) {
	m.listCardsArgs = append(m.listCardsArgs, struct{ listID string }{listID})
	if m.listCardsFn != nil {
		return m.listCardsFn(ctx, listID)
	}
	return []Card{
		{ID: "a1", Name: "Task one", ShortLink: "a1", ShortURL: "ua"},
		{ID: "a2", Name: "Task two", ShortLink: "a2", ShortURL: "ub"},
	}, nil
}

func (m *mockClient) CompleteCard(ctx context.Context, cardID string) (Card, error) {
	m.completeCardArgs = append(m.completeCardArgs, struct{ cardID string }{cardID})
	if m.completeCardFn != nil {
		return m.completeCardFn(ctx, cardID)
	}
	return Card{ID: cardID, Name: "done", ShortLink: cardID, IDList: "done1", ShortURL: "u"}, nil
}

// testResolver returns a Resolver with the captain's sample board layout, using
// clearly-fake 24-char hex fixtures (NOT real ids).
func testResolver() *Resolver {
	return NewResolver(Credentials{
		ListID:     "0123456789abcdef01234567", // Backlog
		DoneListID: "0123456789abcdef0123456d", // Done
		Lists: map[string]string{
			"Backlog": "0123456789abcdef01234567",
			"Julia":   "0123456789abcdef0123456a",
			"Ilya":    "0123456789abcdef0123456b",
			"Teo":     "0123456789abcdef0123456c",
			"Done":    "0123456789abcdef0123456d",
		},
	})
}

// handlerRequest builds a CallToolRequest as the agent would send.
func handlerRequest(name string, args map[string]any) gmcp.CallToolRequest {
	return gmcp.CallToolRequest{
		Params: gmcp.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	}
}

func TestAddCardHandler(t *testing.T) {
	r := testResolver()
	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
		errSub  string
		wantSub string
	}{
		{
			name:    "success",
			args:    map[string]any{"title": "Buy milk", "description": "2%"},
			wantSub: "Created card",
		},
		{
			name:    "success no description",
			args:    map[string]any{"title": "Walk dog"},
			wantSub: "Created card",
		},
		{
			name:    "empty title",
			args:    map[string]any{"title": "  "},
			wantErr: true,
			errSub:  "empty title",
		},
		{
			name:    "missing title arg",
			args:    map[string]any{},
			wantErr: true,
			errSub:  "empty title",
		},
		{
			name:    "client error surfaces",
			args:    map[string]any{"title": "x"},
			wantErr: true,
			errSub:  "simulated failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := &mockClient{}
			if tt.name == "client error surfaces" {
				mc.addCardFn = func(context.Context, string, string, string) (Card, error) {
					return Card{}, errSentinel
				}
			}
			h := addCardHandler(mc, r)
			res, _ := h(context.Background(), handlerRequest(ToolAddCard, tt.args))
			if tt.wantErr {
				if res == nil {
					t.Fatal("expected non-nil result for error")
				}
				if !res.IsError {
					t.Error("expected IsError=true")
				}
				text := toolText(res)
				if !strings.Contains(text, tt.errSub) {
					t.Errorf("error text %q does not contain %q", text, tt.errSub)
				}
				return
			}
			if res == nil {
				t.Fatal("expected non-nil result")
			}
			if res.IsError {
				t.Errorf("unexpected error: %s", toolText(res))
			}
			if !strings.Contains(toolText(res), tt.wantSub) {
				t.Errorf("text %q does not contain %q", toolText(res), tt.wantSub)
			}
			// Verify the title was forwarded.
			if len(mc.addCardArgs) != 1 {
				t.Fatalf("expected 1 AddCard call, got %d", len(mc.addCardArgs))
			}
		})
	}
}

// TestAddCardHandler_Dedup verifies idempotency: adding a card whose title
// already exists on the open list returns the existing card instead of
// creating a duplicate, and makes no AddCard API call.
func TestAddCardHandler_Dedup(t *testing.T) {
	r := testResolver()
	tests := []struct {
		name        string
		existing    []Card
		title       string
		wantDupID   string
		wantNoAdd   bool
		titleMutate string // alternate title that should NOT be a dup
	}{
		{
			name:      "exact title dup",
			existing:  []Card{{ID: "dup1", Name: "Buy milk", ShortLink: "dup1", ShortURL: "u"}},
			title:     "Buy milk",
			wantDupID: "dup1",
			wantNoAdd: true,
		},
		{
			name:      "case + whitespace normalized",
			existing:  []Card{{ID: "dup2", Name: "Buy Milk", ShortLink: "dup2", ShortURL: "u"}},
			title:     "  buy   MILK ",
			wantDupID: "dup2",
			wantNoAdd: true,
		},
		{
			name:      "closed card is not a dup",
			existing:  []Card{{ID: "closed1", Name: "Buy milk", Closed: true, ShortLink: "closed1", ShortURL: "u"}},
			title:     "Buy milk",
			wantDupID: "", // should create
			wantNoAdd: false,
		},
		{
			name:      "different title is not a dup",
			existing:  []Card{{ID: "other1", Name: "Walk dog", ShortLink: "other1", ShortURL: "u"}},
			title:     "Buy milk",
			wantDupID: "",
			wantNoAdd: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := &mockClient{
				listCardsFn: func(context.Context, string) ([]Card, error) { return tt.existing, nil },
			}
			h := addCardHandler(mc, r)
			res, _ := h(context.Background(), handlerRequest(ToolAddCard, map[string]any{"title": tt.title}))
			if res == nil {
				t.Fatal("expected non-nil result")
			}
			if tt.wantNoAdd {
				if res.IsError {
					t.Errorf("expected non-error 'already exists' result, got error: %s", toolText(res))
				}
				if !strings.Contains(toolText(res), "already exists") {
					t.Errorf("expected 'already exists' in %q", toolText(res))
				}
				if !strings.Contains(toolText(res), tt.wantDupID) {
					t.Errorf("expected existing id %q in %q", tt.wantDupID, toolText(res))
				}
				if len(mc.addCardArgs) != 0 {
					t.Errorf("expected 0 AddCard calls, got %d (duplicate created!)", len(mc.addCardArgs))
				}
				return
			}
			// Should have created.
			if len(mc.addCardArgs) != 1 {
				t.Fatalf("expected 1 AddCard call, got %d", len(mc.addCardArgs))
			}
		})
	}
}

// TestAddCardHandler_DedupAllowDuplicate verifies allow_duplicate=true forces a
// create even when a duplicate open card exists.
func TestAddCardHandler_DedupAllowDuplicate(t *testing.T) {
	r := testResolver()
	mc := &mockClient{
		listCardsFn: func(context.Context, string) ([]Card, error) {
			return []Card{{ID: "dup1", Name: "Buy milk", ShortLink: "dup1", ShortURL: "u"}}, nil
		},
	}
	h := addCardHandler(mc, r)
	res, _ := h(context.Background(), handlerRequest(ToolAddCard, map[string]any{
		"title": "Buy milk", "allow_duplicate": true,
	}))
	if res == nil || res.IsError {
		t.Fatalf("expected successful create, got: %s", toolText(res))
	}
	if len(mc.addCardArgs) != 1 {
		t.Errorf("expected 1 AddCard call with allow_duplicate, got %d", len(mc.addCardArgs))
	}
}

// TestAddCardHandler_PersonRouting verifies per-person routing picks the right
// list, and that an unmapped person falls back to the default list.
func TestAddCardHandler_PersonRouting(t *testing.T) {
	r := testResolver()
	tests := []struct {
		name         string
		person       string
		wantListID   string
		wantFallback bool
		wantSub      string
	}{
		{
			name:       "julia routes to her list",
			person:     "Julia",
			wantListID: "0123456789abcdef0123456a",
			wantSub:    "Created card",
		},
		{
			name:       "backlog is the default list name",
			person:     "Backlog",
			wantListID: "0123456789abcdef01234567",
			wantSub:    "Created card",
		},
		{
			name:         "unmapped person falls back to default",
			person:       "Nobody",
			wantListID:   "0123456789abcdef01234567",
			wantFallback: true,
			wantSub:      "Created card",
		},
		{
			name:       "empty person uses default",
			person:     "",
			wantListID: "0123456789abcdef01234567",
			wantSub:    "Created card",
		},
		{
			name:       "case-insensitive person match",
			person:     "julia",
			wantListID: "0123456789abcdef0123456a",
			wantSub:    "Created card",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := &mockClient{} // ListCards nil -> 2 cards, no dup
			h := addCardHandler(mc, r)
			args := map[string]any{"title": "Task"}
			if tt.person != "" {
				args["person"] = tt.person
			}
			res, _ := h(context.Background(), handlerRequest(ToolAddCard, args))
			if res == nil {
				t.Fatal("expected non-nil result")
			}
			if res.IsError {
				t.Fatalf("unexpected error: %s", toolText(res))
			}
			if !strings.Contains(toolText(res), tt.wantSub) {
				t.Errorf("text %q does not contain %q", toolText(res), tt.wantSub)
			}
			if len(mc.addCardArgs) != 1 {
				t.Fatalf("expected 1 AddCard call, got %d", len(mc.addCardArgs))
			}
			if mc.addCardArgs[0].listID != tt.wantListID {
				t.Errorf("listID = %q, want %q", mc.addCardArgs[0].listID, tt.wantListID)
			}
			if tt.wantFallback && !strings.Contains(toolText(res), "not a configured person/list") {
				t.Errorf("expected fallback note in %q", toolText(res))
			}
			if !tt.wantFallback && strings.Contains(toolText(res), "not a configured person/list") {
				t.Errorf("did not expect fallback note in %q", toolText(res))
			}
		})
	}
}

// TestAddCardHandler_StripsForInTitle verifies the model is prevented from
// burying the target in the title: "(For Julia)" is stripped when person=Julia.
func TestAddCardHandler_StripsForInTitle(t *testing.T) {
	r := testResolver()
	var gotTitle, gotListID string
	mc := &mockClient{
		addCardFn: func(_ context.Context, listID, title, _ string) (Card, error) {
			gotTitle = title
			gotListID = listID
			return Card{ID: "x1", Name: title, ShortLink: "x1", IDList: listID, ShortURL: "u"}, nil
		},
		listCardsFn: func(context.Context, string) ([]Card, error) { return nil, nil },
	}
	h := addCardHandler(mc, r)
	res, _ := h(context.Background(), handlerRequest(ToolAddCard, map[string]any{
		"title":  "create trello token (For Julia)",
		"person": "Julia",
	}))
	if res == nil || res.IsError {
		t.Fatalf("expected success: %s", toolText(res))
	}
	if gotTitle != "create trello token" {
		t.Errorf("title = %q, want %q", gotTitle, "create trello token")
	}
	if gotListID != "0123456789abcdef0123456a" {
		t.Errorf("listID = %q, want Julia id", gotListID)
	}
}

func TestListCardsHandler(t *testing.T) {
	r := testResolver()
	t.Run("success lists cards", func(t *testing.T) {
		mc := &mockClient{}
		h := listCardsHandler(mc, r)
		res, _ := h(context.Background(), handlerRequest(ToolListCards, nil))
		if res == nil || res.IsError {
			t.Fatalf("unexpected error: %v", res)
		}
		text := toolText(res)
		if !strings.Contains(text, "Task one") {
			t.Errorf("text missing 'Task one': %q", text)
		}
		if !strings.Contains(text, "a2") {
			t.Errorf("text missing shortLink 'a2': %q", text)
		}
	})

	t.Run("empty list message", func(t *testing.T) {
		mc := &mockClient{
			listCardsFn: func(context.Context, string) ([]Card, error) { return nil, nil },
		}
		h := listCardsHandler(mc, r)
		res, _ := h(context.Background(), handlerRequest(ToolListCards, nil))
		if res == nil || res.IsError {
			t.Fatalf("unexpected error: %v", res)
		}
		if !strings.Contains(toolText(res), "empty") {
			t.Errorf("expected 'empty' message, got %q", toolText(res))
		}
	})

	t.Run("client error", func(t *testing.T) {
		mc := &mockClient{
			listCardsFn: func(context.Context, string) ([]Card, error) { return nil, errSentinel },
		}
		h := listCardsHandler(mc, r)
		res, _ := h(context.Background(), handlerRequest(ToolListCards, nil))
		if res == nil || !res.IsError {
			t.Fatal("expected error result")
		}
		if !strings.Contains(toolText(res), "simulated failure") {
			t.Errorf("missing error detail: %q", toolText(res))
		}
	})

	// A non-hex list_id that is not a known list name must error and make NO
	// API call — this is the core of "never fail silently."
	t.Run("non-hex list_id errors without API call", func(t *testing.T) {
		mc := &mockClient{}
		h := listCardsHandler(mc, r)
		res, _ := h(context.Background(), handlerRequest(ToolListCards, map[string]any{
			"list_id": "NotAValidList",
		}))
		if res == nil || !res.IsError {
			t.Fatal("expected error result for non-hex list_id")
		}
		if len(mc.listCardsArgs) != 0 {
			t.Errorf("expected 0 ListCards calls (no API call), got %d", len(mc.listCardsArgs))
		}
		text := toolText(res)
		if !strings.Contains(text, "not a 24-char hex") {
			t.Errorf("error %q does not explain the hex requirement", text)
		}
		if !strings.Contains(text, "valid lists") {
			t.Errorf("error %q does not list valid lists", text)
		}
	})

	// A valid list name resolves to its id and is forwarded to the API.
	t.Run("valid list name resolves to id", func(t *testing.T) {
		mc := &mockClient{}
		h := listCardsHandler(mc, r)
		res, _ := h(context.Background(), handlerRequest(ToolListCards, map[string]any{
			"list_id": "Julia",
		}))
		if res == nil || res.IsError {
			t.Fatalf("unexpected error: %s", toolText(res))
		}
		if len(mc.listCardsArgs) != 1 {
			t.Fatalf("expected 1 ListCards call, got %d", len(mc.listCardsArgs))
		}
		if mc.listCardsArgs[0].listID != "0123456789abcdef0123456a" {
			t.Errorf("listID = %q, want Julia id", mc.listCardsArgs[0].listID)
		}
		if !strings.Contains(toolText(res), "Task one") {
			t.Errorf("expected card in result: %q", toolText(res))
		}
	})

	// A 24-char hex id that is NOT one of the configured lists must error —
	// this catches the "wrong id failed silently" spiral.
	t.Run("unknown hex list_id errors without API call", func(t *testing.T) {
		mc := &mockClient{}
		h := listCardsHandler(mc, r)
		res, _ := h(context.Background(), handlerRequest(ToolListCards, map[string]any{
			"list_id": "68e15d40a06fb18420cb0e21",
		}))
		if res == nil || !res.IsError {
			t.Fatal("expected error for unknown hex list_id")
		}
		if len(mc.listCardsArgs) != 0 {
			t.Errorf("expected 0 ListCards calls, got %d", len(mc.listCardsArgs))
		}
		if !strings.Contains(toolText(res), "valid-format id but is not one of the configured lists") {
			t.Errorf("error %q does not explain the config mismatch", toolText(res))
		}
	})
}

func TestCompleteCardHandler(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mc := &mockClient{}
		h := completeCardHandler(mc)
		res, _ := h(context.Background(), handlerRequest(ToolCompleteCard, map[string]any{"card_id": "abc"}))
		if res == nil || res.IsError {
			t.Fatalf("unexpected error: %v", res)
		}
		if !strings.Contains(toolText(res), "Completed card") {
			t.Errorf("missing 'Completed card': %q", toolText(res))
		}
		if len(mc.completeCardArgs) != 1 || mc.completeCardArgs[0].cardID != "abc" {
			t.Errorf("card_id not forwarded: %+v", mc.completeCardArgs)
		}
	})

	t.Run("empty card_id", func(t *testing.T) {
		mc := &mockClient{}
		h := completeCardHandler(mc)
		res, _ := h(context.Background(), handlerRequest(ToolCompleteCard, map[string]any{"card_id": "  "}))
		if res == nil || !res.IsError {
			t.Fatal("expected error")
		}
		if !strings.Contains(toolText(res), "card_id is required") {
			t.Errorf("missing msg: %q", toolText(res))
		}
		if len(mc.completeCardArgs) != 0 {
			t.Error("should not have called CompleteCard")
		}
	})
}

// errSentinel is a reusable error for handler tests.
var errSentinel = fmt.Errorf("simulated failure")

// toolText extracts the concatenated text from a CallToolResult.
func toolText(res *gmcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(gmcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// TestServerEndToEnd wires the handlers through a real mcp-go in-process server
// and exercises the full MCP protocol (initialize, list tools, call tools).
func TestServerEndToEnd(t *testing.T) {
	mc := &mockClient{}
	srv := NewServerWithClient(mc, testResolver())

	client, err := gclient.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	defer client.Close()

	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	initReq := gmcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = gmcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = gmcp.Implementation{Name: "test", Version: "1.0"}
	if _, err := client.Initialize(context.Background(), initReq); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// List tools — expect exactly the three Trello tools.
	toolList, err := client.ListTools(context.Background(), gmcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	gotNames := map[string]bool{}
	for _, tl := range toolList.Tools {
		gotNames[tl.Name] = true
	}
	for _, want := range []string{ToolAddCard, ToolListCards, ToolCompleteCard} {
		if !gotNames[want] {
			t.Errorf("tool %q not registered", want)
		}
	}
	if len(toolList.Tools) != 3 {
		t.Errorf("expected 3 tools, got %d", len(toolList.Tools))
	}

	// Call add_card.
	addReq := gmcp.CallToolRequest{
		Params: gmcp.CallToolParams{
			Name:      ToolAddCard,
			Arguments: map[string]any{"title": "End to end card", "description": "via mcp"},
		},
	}
	addRes, err := client.CallTool(context.Background(), addReq)
	if err != nil {
		t.Fatalf("CallTool(add): %v", err)
	}
	if addRes.IsError {
		t.Fatalf("add_card returned error: %s", toolText(addRes))
	}
	if len(mc.addCardArgs) != 1 {
		t.Errorf("expected 1 AddCard call via MCP, got %d", len(mc.addCardArgs))
	}

	// Call list_cards.
	listReq := gmcp.CallToolRequest{
		Params: gmcp.CallToolParams{Name: ToolListCards},
	}
	listRes, err := client.CallTool(context.Background(), listReq)
	if err != nil {
		t.Fatalf("CallTool(list): %v", err)
	}
	if listRes.IsError {
		t.Fatalf("list_cards returned error: %s", toolText(listRes))
	}
	if !strings.Contains(toolText(listRes), "Task one") {
		t.Errorf("list_cards did not surface card: %q", toolText(listRes))
	}

	// Call complete_card.
	compReq := gmcp.CallToolRequest{
		Params: gmcp.CallToolParams{
			Name:      ToolCompleteCard,
			Arguments: map[string]any{"card_id": "abc"},
		},
	}
	compRes, err := client.CallTool(context.Background(), compReq)
	if err != nil {
		t.Fatalf("CallTool(complete): %v", err)
	}
	if compRes.IsError {
		t.Fatalf("complete_card returned error: %s", toolText(compRes))
	}
	if len(mc.completeCardArgs) != 1 || mc.completeCardArgs[0].cardID != "abc" {
		t.Errorf("complete_card args not forwarded: %+v", mc.completeCardArgs)
	}
}
