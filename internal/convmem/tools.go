// Package convmem implements the Tier 1 conversation-recall builtin
// (search_conversations): FTS5 keyword search over the current user's own
// message history. Scope is enforced server-side — the user name is passed
// by the dispatch layer and never read from LLM-supplied arguments.
package convmem

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/store"
)

// Tool name for the builtin conversation-recall tool.
const toolNameSearchConversations = "builtin__search_conversations"

// SearchDefinition returns the tool definition for search_conversations.
func SearchDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name: toolNameSearchConversations,
		Description: "Search your own past conversations with the assistant for keywords or " +
			"phrases. Optionally limit to a date range. Returns matching messages with the " +
			"conversation id, role, date, and a snippet of the match. Use this to recall what " +
			"you or the user said earlier, decisions made, or prior requests.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "FTS query terms/phrases; wrap exact phrases in double quotes",
				},
				"from_date": map[string]any{
					"type":        "string",
					"description": "Optional inclusive lower bound, YYYY-MM-DD",
				},
				"to_date": map[string]any{
					"type":        "string",
					"description": "Optional inclusive upper bound, YYYY-MM-DD",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Default 10, max 25",
				},
			},
			"required": []string{"query"},
		},
		Source: "builtin",
		// Explicit role enumeration, matching the user-memory idiom
		// (internal/usermemory/tools.go): all 5 roles.
		Roles: []string{"parent", "child", "age_8_12", "age_13_17", "under_8"},
	}
}

// HandleSearch dispatches the search_conversations tool.
//
// userName is the authoritative scope: it is the agent user's name passed
// by the dispatch layer, never a value from LLM-supplied args, so the call
// can only ever return the caller's own messages.
func HandleSearch(ctx context.Context, db *store.DB, userName, query, fromDate, toDate string, maxResults int) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is required")
	}
	hits, err := db.SearchConversations(ctx, userName, query, fromDate, toDate, maxResults)
	if err != nil {
		return "", fmt.Errorf("search conversations: %w", err)
	}
	if len(hits) == 0 {
		return "No matches.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d matches for %q:\n", len(hits), query)
	for _, h := range hits {
		var when string
		if !h.CreatedAt.IsZero() {
			when = h.CreatedAt.Format(time.DateOnly)
		}
		fmt.Fprintf(&b, "- [conv %s] %s %s: %s\n", h.ConversationID, h.Role, when, h.Snippet)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
