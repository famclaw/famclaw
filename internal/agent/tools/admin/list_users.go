package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/famclaw/famclaw/internal/agentcore"
)

const toolNameListUsers = "builtin__list_users"

// ListUsersDefinition returns the agentcore.Tool definition for the list_users tool.
func ListUsersDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameListUsers,
		Description: "List all configured family members with their linked gateway accounts and any active role overrides.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleListUsers executes the list_users tool.
// Returns a JSON-encoded slice of user summaries sorted by name.
func HandleListUsers(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	if err := logAudit(ctx, deps, toolNameListUsers, args); err != nil {
		fmt.Fprintf(os.Stderr, "[admin] audit log failed: %v\n", err)
	}

	users := deps.Cfg.Users

	// Sort by name for deterministic output.
	sorted := make([]struct {
		name        string
		displayName string
		role        string
		ageGroup    string
	}, 0, len(users))
	for _, u := range users {
		sorted = append(sorted, struct {
			name        string
			displayName string
			role        string
			ageGroup    string
		}{u.Name, u.DisplayName, u.Role, u.AgeGroup})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].name < sorted[j].name
	})

	result := make([]map[string]any, 0, len(sorted))
	for _, u := range sorted {
		// Fetch linked gateway accounts.
		accounts, err := deps.DB.ListGatewayAccountsByUser(ctx, u.name)
		if err != nil {
			return "", fmt.Errorf("list gateway accounts for %q: %w", u.name, err)
		}
		linkedGateways := make([]string, 0, len(accounts))
		for _, acc := range accounts {
			linkedGateways = append(linkedGateways, acc.Gateway+":"+acc.ExternalID)
		}

		// Check for a role override.
		overrideRole, _, err := deps.DB.GetRoleOverride(ctx, u.name)
		if err != nil {
			return "", fmt.Errorf("get role override for %q: %w", u.name, err)
		}
		hasRoleOverride := overrideRole != ""

		result = append(result, map[string]any{
			"name":             u.name,
			"display_name":     u.displayName,
			"role":             u.role,
			"age_group":        u.ageGroup,
			"linked_gateways":  linkedGateways,
			"has_role_override": hasRoleOverride,
		})
	}

	b, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal users list: %w", err)
	}
	return string(b), nil
}
