package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/famclaw/famclaw/internal/agentcore"
)

const toolNameListUnknownAccounts = "builtin__list_unknown_accounts"

// ListUnknownAccountsDefinition returns the agentcore.Tool definition for the
// list_unknown_accounts tool.
func ListUnknownAccountsDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameListUnknownAccounts,
		Description: "List all unlinked (unknown) gateway accounts that have contacted the bot but are not yet associated with a family member.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleListUnknownAccounts executes the list_unknown_accounts tool.
// Returns a JSON-encoded slice of unknown account summaries.
func HandleListUnknownAccounts(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	if err := logAudit(ctx, deps, toolNameListUnknownAccounts, args); err != nil {
		fmt.Fprintf(os.Stderr, "[admin] audit log failed: %v\n", err)
	}

	accounts, err := deps.DB.ListUnknownAccounts(ctx)
	if err != nil {
		return "", fmt.Errorf("list unknown accounts: %w", err)
	}

	result := make([]map[string]any, 0, len(accounts))
	for _, u := range accounts {
		result = append(result, map[string]any{
			"id":           u.ID,
			"gateway":      u.Gateway,
			"external_id":  u.ExternalID,
			"display_name": u.DisplayName,
			"first_seen":   u.FirstSeen.UTC().Format("2006-01-02T15:04:05Z"),
			"last_seen":    u.LastSeen.UTC().Format("2006-01-02T15:04:05Z"),
			"attempts":     u.Attempts,
		})
	}

	b, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal unknown accounts: %w", err)
	}
	return string(b), nil
}
