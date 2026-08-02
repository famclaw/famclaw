package trello

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Tool names. These MUST match the tool names referenced in SKILL.md, because
// famclaw injects the SKILL.md body into the system prompt (telling the model
// which tools to call) and the MCP pool exposes these exact names to the
// agent's tool loop.
const (
	ToolAddCard      = "trello_add_card"
	ToolListCards    = "trello_list_cards"
	ToolCompleteCard = "trello_complete_card"
)

// addCardArgs are the arguments for trello_add_card.
type addCardArgs struct {
	Title string `json:"title"`
	// Description is optional longer detail for the card.
	Description string `json:"description,omitempty"`
	// Person is the target person (or list name) for this card, e.g. "Julia"
	// or "Backlog". When set, the card is routed to that person's list. When
	// omitted, the card goes to the default list (TRELLO_LIST_ID / Backlog).
	// See Resolver.ResolvePerson.
	Person string `json:"person,omitempty"`
	// AllowDuplicate, when true, skips the idempotency check and creates the
	// card even if an open card with the same title already exists on the
	// target list. Defaults to false.
	AllowDuplicate bool `json:"allow_duplicate,omitempty"`
}

// listCardsArgs are the arguments for trello_list_cards.
type listCardsArgs struct {
	// ListID is an optional list name (e.g. "Backlog", "Julia") or a 24-char
	// hex list id. Omit to use the default list.
	ListID string `json:"list_id,omitempty"`
}

// completeCardArgs are the arguments for trello_complete_card.
type completeCardArgs struct {
	// CardID is a Trello card ID or short link (as shown by trello_list_cards).
	CardID string `json:"card_id"`
}

// toolDescription strings are kept as package-level constants so the SKILL.md
// (which the model reads) and the live tool schema agree on wording.

// addCardTool builds the add_card tool definition. personNames are the valid
// person/list names from the configured TRELLO_LISTS map; they are exposed as
// a JSON-schema enum so the model is guided toward valid choices.
func addCardTool(lists map[string]string) mcp.Tool {
	personOpts := []mcp.PropertyOption{
		mcp.Description("Target person (or list name) for this card, from skills.credentials.TRELLO_LISTS (e.g. Julia, Ilya, Teo, Backlog). The card lands in that person's list, never the shared Backlog. When omitted, the card goes to the default list. You know who you are talking to — use that name for the speaker's own tasks."),
	}
	if names := sortedNames(lists); len(names) > 0 {
		personOpts = append(personOpts, mcp.Enum(names...))
	}
	return mcp.NewTool(ToolAddCard,
		mcp.WithDescription("Create a card on a Trello list. Set `person` to the family member this task is for — the card lands in that person's list, not the shared Backlog. If the task is for the speaker (the family member you're talking to), set person to that person's name. If `person` is omitted or not a configured person, the card goes to the default list (TRELLO_LIST_ID / Backlog). If an open card with the same title already exists on the target list, the existing card is returned instead of creating a duplicate; set allow_duplicate=true to force a new card. The title should be the task only — do not bury the target in it."),
		mcp.WithString("title",
			mcp.Description("What the card is about — the todo item itself. Do not include the target person here; use the `person` argument."),
			mcp.Required(),
		),
		mcp.WithString("description",
			mcp.Description("Optional longer detail for the card."),
		),
		mcp.WithString("person", personOpts...),
		mcp.WithBoolean("allow_duplicate",
			mcp.Description("If true, create the card even if an open card with the same title already exists on the target list."),
			mcp.DefaultBool(false),
		),
	)
}

// listCardsTool builds the list_cards tool definition. listNames are the valid
// list names from TRELLO_LISTS, exposed as an enum.
func listCardsTool(lists map[string]string) mcp.Tool {
	opts := []mcp.PropertyOption{
		mcp.Description("List name (e.g. Backlog, Julia, Done) or a 24-char hex list id. Omit to use the default list. Must be a known name or a valid-format id present in skills.credentials.TRELLO_LISTS."),
	}
	if names := sortedNames(lists); len(names) > 0 {
		opts = append(opts, mcp.Enum(names...))
	}
	return mcp.NewTool(ToolListCards,
		mcp.WithDescription("List the cards currently on a Trello list. Call this when the user asks what's on their todo, to see their tasks, or to find a card before completing it."),
		mcp.WithString("list_id", opts...),
	)
}

// completeCardTool builds the complete_card tool definition.
func completeCardTool() mcp.Tool {
	return mcp.NewTool(ToolCompleteCard,
		mcp.WithDescription("Move a card to the done list, marking it complete. Call this when the user says they finished, completed, or did a task — e.g. \"I did X\", \"mark X done\", \"X is finished\"."),
		mcp.WithString("card_id",
			mcp.Description("The Trello card ID or short link, as returned by trello_list_cards."),
			mcp.Required(),
		),
	)
}

// addCardHandler returns an MCP handler that creates a card with per-person
// routing and idempotency.
func addCardHandler(c Client, r *Resolver) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args addCardArgs
		if err := req.BindArguments(&args); err != nil {
			return mcp.NewToolResultErrorFromErr("invalid arguments: %w", err), nil
		}
		if strings.TrimSpace(args.Title) == "" {
			return mcp.NewToolResultError("cannot add a card with an empty title"), nil
		}

		// Resolve the target list from the person/list name (priority:
		// explicit target > default). An unknown person falls back to the
		// default list but is flagged so we can report it — never silently.
		listID, matchedName, fallback, err := r.ResolvePerson(args.Person)
		if err != nil {
			return toolError("resolving target list", err), nil
		}

		// Strip a "(For <name>)" suffix so the title stays clean when a
		// person was resolved.
		title := cleanTitle(args.Title, r.Lists)

		// Idempotency: before creating, check whether an open card with the
		// same title already exists on the target list. This is what prevents
		// the duplicate-card spiral — a retry no longer creates a second card.
		cards, lerr := c.ListCards(ctx, listID)
		if lerr != nil {
			return toolError("checking for existing card", lerr), nil
		}
		if !args.AllowDuplicate {
			if dup := findDuplicate(cards, title); dup != nil {
				return mcp.NewToolResultText(formatCardDuplicate(dup, matchedName)), nil
			}
		}

		card, err := c.AddCard(ctx, listID, title, args.Description)
		if err != nil {
			return toolError("creating card", err), nil
		}
		result := formatCardCreated(card, matchedName)
		if fallback {
			result += fmt.Sprintf("\nNote: %q is not a configured person/list; placed on the default list (valid names: %s).", args.Person, r.formatValidLists())
		}
		return mcp.NewToolResultText(result), nil
	}
}

// listCardsHandler returns an MCP handler that lists cards. The list_id may be
// a name or a 24-char hex id; it is validated by the Resolver before any API
// call, so a garbage value never reaches Trello.
func listCardsHandler(c Client, r *Resolver) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args listCardsArgs
		if err := req.BindArguments(&args); err != nil {
			return mcp.NewToolResultErrorFromErr("invalid arguments: %w", err), nil
		}
		listID, matched, err := r.ResolveList(args.ListID)
		if err != nil {
			return toolError("resolving list", err), nil
		}
		cards, err := c.ListCards(ctx, listID)
		if err != nil {
			return toolError("listing cards", err), nil
		}
		return mcp.NewToolResultText(formatCardList(cards, matched)), nil
	}
}

// completeCardHandler returns an MCP handler that completes a card.
func completeCardHandler(c Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args completeCardArgs
		if err := req.BindArguments(&args); err != nil {
			return mcp.NewToolResultErrorFromErr("invalid arguments: %w", err), nil
		}
		if strings.TrimSpace(args.CardID) == "" {
			return mcp.NewToolResultError("card_id is required"), nil
		}
		card, err := c.CompleteCard(ctx, args.CardID)
		if err != nil {
			return toolError("completing card", err), nil
		}
		return mcp.NewToolResultText(formatCardCompleted(card)), nil
	}
}

// RegisterTools adds the Trello tools to an MCP server. The Resolver wires
// name resolution and per-person routing into the handlers.
func RegisterTools(srv *server.MCPServer, c Client, r *Resolver) {
	srv.AddTool(addCardTool(r.Lists), addCardHandler(c, r))
	srv.AddTool(listCardsTool(r.Lists), listCardsHandler(c, r))
	srv.AddTool(completeCardTool(), completeCardHandler(c))
}

// NewServer builds an MCPServer backed by a real Trello HTTP client and a
// Resolver from the given credentials.
func NewServer(creds Credentials) *server.MCPServer {
	return NewServerWithClient(NewHTTPClient(creds), NewResolver(creds))
}

// NewServerWithClient builds an MCPServer using the given Client and Resolver.
// This is the testable seam — tests pass a mock Client and a test Resolver.
func NewServerWithClient(c Client, r *Resolver) *server.MCPServer {
	srv := server.NewMCPServer("trello-skill", "0.1.0")
	RegisterTools(srv, c, r)
	return srv
}

// toolError returns an IsError result describing a tool failure.
func toolError(action string, err error) *mcp.CallToolResult {
	return mcp.NewToolResultErrorFromErr(fmt.Sprintf("trello: %s failed", action), err)
}

func formatCardCreated(card Card, listName string) string {
	return fmt.Sprintf("Created card %q on list %s.\nid=%s  shortLink=%s  url=%s",
		card.Name, listName, card.ID, card.ShortLink, card.ShortURL)
}

// formatCardDuplicate formats the "already exists" result returned instead of
// creating a duplicate card. It is a non-error result so the model does not
// retry.
func formatCardDuplicate(card *Card, listName string) string {
	return fmt.Sprintf("Card already exists on list %s — not creating a duplicate.\n%s  title=%q\nTo add another card with this title, set allow_duplicate=true.",
		listName, formatCardRef(card), card.Name)
}

// formatCardRef is the shared id/shortLink/url line.
func formatCardRef(card *Card) string {
	return fmt.Sprintf("id=%s  shortLink=%s  url=%s", card.ID, card.ShortLink, card.ShortURL)
}

func formatCardList(cards []Card, listName string) string {
	if len(cards) == 0 {
		return fmt.Sprintf("The list (%s) is empty — there are no todo cards.", listName)
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Listing %d card(s) on list %s:\n", len(cards), listName))
	for i := range cards {
		c := cards[i]
		status := ""
		if c.Closed {
			status = " [closed]"
		}
		b.WriteString(fmt.Sprintf("%d. %s%s  (id=%s, shortLink=%s)\n   %s\n",
			i+1, c.Name, status, c.ID, c.ShortLink, c.ShortURL))
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatCardCompleted(card Card) string {
	return fmt.Sprintf("Completed card %q — moved to the done list.\nid=%s  shortLink=%s  url=%s",
		card.Name, card.ID, card.ShortLink, card.ShortURL)
}

// findDuplicate returns a pointer to the first open card whose title matches
// the given title (case-insensitive, whitespace-normalized), or nil.
func findDuplicate(cards []Card, title string) *Card {
	want := normalizeTitle(title)
	for i := range cards {
		if cards[i].Closed {
			continue
		}
		if normalizeTitle(cards[i].Name) == want {
			return &cards[i]
		}
	}
	return nil
}

// normalizeTitle lowercases and collapses internal whitespace so "Buy milk"
// and "  buy   milk " are treated as the same title.
func normalizeTitle(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
