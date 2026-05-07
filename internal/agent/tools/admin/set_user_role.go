package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/famclaw/famclaw/internal/agentcore"
)

const toolNameSetUserRole = "builtin__set_user_role"

// validRoles is the set of roles that may be assigned via this tool.
var validRoles = map[string]bool{
	"parent":    true,
	"age_13_17": true,
	"age_8_12":  true,
	"under_8":   true,
}

// SetUserRoleDefinition returns the agentcore.Tool definition for the
// set_user_role tool.
func SetUserRoleDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameSetUserRole,
		Description: "Override the role and age group for a family member. Valid roles: parent, age_13_17, age_8_12, under_8.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user_name": map[string]any{
					"type":        "string",
					"description": "User name of the family member to update",
				},
				"role": map[string]any{
					"type":        "string",
					"description": "New role: parent | age_13_17 | age_8_12 | under_8",
					"enum":        []string{"parent", "age_13_17", "age_8_12", "under_8"},
				},
			},
			"required": []string{"user_name", "role"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleSetUserRole executes the set_user_role tool.
func HandleSetUserRole(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	userName, _ := args["user_name"].(string)
	if userName == "" {
		return "", fmt.Errorf("set_user_role: user_name is required")
	}

	found := false
	for _, u := range deps.Cfg.Users {
		if u.Name == userName {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("set_user_role: user %q not found in config", userName)
	}

	role, _ := args["role"].(string)
	if !validRoles[role] {
		return "", fmt.Errorf("set_user_role: invalid role %q — must be one of: parent, age_13_17, age_8_12, under_8", role)
	}

	// age_group enumerates child cohorts only — for child roles it
	// matches the role string (under_8 / age_8_12 / age_13_17). Parents
	// have no age cohort, so we leave it empty rather than leaking the
	// literal "parent" into downstream age-gating logic.
	ageGroup := role
	if role == "parent" {
		ageGroup = ""
	}

	if err := deps.DB.SetRoleOverride(ctx, userName, role, ageGroup, deps.Actor); err != nil {
		return "", fmt.Errorf("set_user_role: %w", err)
	}

	if err := logAudit(ctx, deps, toolNameSetUserRole, args); err != nil {
		fmt.Fprintf(os.Stderr, "[admin] audit log failed: %v\n", err)
	}

	result := map[string]any{
		"user_name": userName,
		"role":      role,
		"age_group": ageGroup,
		"set_by":    deps.Actor,
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("set_user_role: marshal result: %w", err)
	}
	return string(b), nil
}
