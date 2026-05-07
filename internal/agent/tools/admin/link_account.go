package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/famclaw/famclaw/internal/agentcore"
)

const toolNameLinkAccount = "builtin__link_account"

// LinkAccountDefinition returns the agentcore.Tool definition for the
// link_account tool.
func LinkAccountDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameLinkAccount,
		Description: "Link an unknown gateway account to an existing family member. Use list_unknown_accounts first to get the account_id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account_id": map[string]any{
					"type":        "integer",
					"description": "ID of the unknown account to link (from list_unknown_accounts)",
				},
				"user_name": map[string]any{
					"type":        "string",
					"description": "Name of the target family member",
				},
			},
			"required": []string{"account_id", "user_name"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleLinkAccount executes the link_account tool.
func HandleLinkAccount(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	// JSON numbers decode as float64.
	accountIDFloat, ok := args["account_id"].(float64)
	if !ok {
		return "", fmt.Errorf("link_account: account_id is required and must be a number")
	}
	accountID := int64(accountIDFloat)
	if accountID <= 0 {
		return "", fmt.Errorf("link_account: account_id must be a positive integer")
	}

	userName, _ := args["user_name"].(string)
	if userName == "" {
		return "", fmt.Errorf("link_account: user_name is required")
	}

	userFound := false
	for _, u := range deps.Cfg.Users {
		if u.Name == userName {
			userFound = true
			break
		}
	}
	if !userFound {
		return "", fmt.Errorf("link_account: user %q not found in config", userName)
	}

	accounts, err := deps.DB.ListUnknownAccounts(ctx)
	if err != nil {
		return "", fmt.Errorf("link_account: list unknown accounts: %w", err)
	}

	var found *struct {
		Gateway    string
		ExternalID string
	}
	for _, u := range accounts {
		if u.ID == accountID {
			found = &struct {
				Gateway    string
				ExternalID string
			}{Gateway: u.Gateway, ExternalID: u.ExternalID}
			break
		}
	}
	if found == nil {
		return "", fmt.Errorf("link_account: account_id %d not found", accountID)
	}

	if err := deps.DB.LinkAndClearUnknownAccount(ctx, userName, found.Gateway, found.ExternalID); err != nil {
		return "", fmt.Errorf("link_account: %w", err)
	}

	if err := logAudit(ctx, deps, toolNameLinkAccount, args); err != nil {
		fmt.Fprintf(os.Stderr, "[admin] audit log failed: %v\n", err)
	}

	result := map[string]any{
		"account_id":  accountID,
		"user_name":   userName,
		"gateway":     found.Gateway,
		"external_id": found.ExternalID,
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("link_account: marshal result: %w", err)
	}
	return string(b), nil
}
