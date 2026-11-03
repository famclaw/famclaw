package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/famclaw/famclaw/internal/agentcore"
)

const toolNameSetUserRole = "builtin__set_user_role"

// validRoles is the shared role schema the policy engine understands
// (Terra F12): role is "parent" or "child" — never a cohort value.
var validRoles = map[string]bool{
	"parent": true,
	"child":  true,
}

// validAgeGroups is the separate age-group cohort dimension.
var validAgeGroups = map[string]bool{
	"under_8":   true,
	"age_8_12":  true,
	"age_13_17": true,
}

// SetUserRoleDefinition returns the agentcore.Tool definition for the
// set_user_role tool.
func SetUserRoleDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameSetUserRole,
		Description: "Override the role or age group for a family member. role: parent | child. age_group (child only, optional): under_8 | age_8_12 | age_13_17. Setting role=child demotes a configured parent (e.g. for parental leave); setting role=parent on a configured parent clears that demotion. A child can never be promoted to parent — the parent role must be set in the household config.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user_name": map[string]any{
					"type":        "string",
					"description": "User name of the family member to update",
				},
				"role": map[string]any{
					"type":        "string",
					"description": "New role: parent | child",
					"enum":        []string{"parent", "child"},
				},
				"age_group": map[string]any{
					"type":        "string",
					"description": "Age cohort for a child role: under_8 | age_8_12 | age_13_17. Omit to keep the configured cohort.",
					"enum":        []string{"under_8", "age_8_12", "age_13_17"},
				},
			},
			"required": []string{"user_name", "role"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleSetUserRole executes the set_user_role tool.
//
// Role and age group are two independent dimensions (Terra F12): role is
// persisted as "parent" or "child" (the schema the policy engine matches),
// and age_group as a cohort. An override may demote a configured parent to
// "child" but may never promote a configured non-parent to "parent"
// (Terra F13). For a configured parent, role=parent is the recovery action:
// it clears any demotion override so the base config role applies again.
func HandleSetUserRole(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	userName, _ := args["user_name"].(string)
	if userName == "" {
		return "", fmt.Errorf("set_user_role: user_name is required")
	}

	baseRole := ""
	found := false
	for _, u := range deps.Cfg.Users {
		if u.Name == userName {
			baseRole = u.Role
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("set_user_role: user %q not found in config", userName)
	}

	role, _ := args["role"].(string)
	if !validRoles[role] {
		return "", fmt.Errorf("set_user_role: invalid role %q — must be one of: parent, child", role)
	}
	ageGroup, _ := args["age_group"].(string)
	if ageGroup != "" && !validAgeGroups[ageGroup] {
		return "", fmt.Errorf("set_user_role: invalid age_group %q — must be one of: under_8, age_8_12, age_13_17", ageGroup)
	}
	if role == "parent" && ageGroup != "" {
		return "", fmt.Errorf("set_user_role: age_group is not allowed with role parent (parents carry no age cohort)")
	}

	switch role {
	case "parent":
		if baseRole != "parent" {
			return "", fmt.Errorf("set_user_role: cannot grant parent role to %q — the user is configured as %q; parent must be set in the household config, overrides may only demote", userName, baseRole)
		}
		// Configured parent: clearing the override is the recovery action —
		// the base config role applies again.
		if err := deps.DB.DeleteRoleOverride(ctx, userName); err != nil {
			return "", fmt.Errorf("set_user_role: %w", err)
		}

	case "child":
		if err := deps.DB.SetRoleOverride(ctx, userName, "child", ageGroup, deps.Actor); err != nil {
			return "", fmt.Errorf("set_user_role: %w", err)
		}
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
