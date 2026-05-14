package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/familystate"
)

const toolNameDeleteFamilyCategory = "builtin__delete_family_category"

// DeleteFamilyCategoryDefinition returns the parent-only tool that
// removes a custom category. Built-in categories cannot be deleted;
// the category must be empty (parent must delete its facts first).
func DeleteFamilyCategoryDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameDeleteFamilyCategory,
		Description: "Delete a custom family-fact category (parent-only). Built-in categories cannot be deleted. The category must be empty (delete its facts first).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Category name to delete."},
			},
			"required": []string{"name"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleDeleteFamilyCategory dispatches builtin__delete_family_category.
func HandleDeleteFamilyCategory(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	if deps.FamilyState == nil {
		return "", fmt.Errorf("delete_family_category: family state is not configured")
	}
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("delete_family_category: name is required")
	}
	if err := deps.FamilyState.DeleteCategory(ctx, name); err != nil {
		switch {
		case errors.Is(err, familystate.ErrBuiltinCategory):
			return "can't delete a built-in category", nil
		case errors.Is(err, familystate.ErrCategoryNotEmpty):
			return fmt.Sprintf("category %q has facts; delete them first", name), nil
		case errors.Is(err, familystate.ErrUnknownCategory):
			return fmt.Sprintf("unknown category %q", name), nil
		default:
			return "", fmt.Errorf("delete_family_category: %w", err)
		}
	}
	auditArgs, _ := json.Marshal(map[string]any{"name": name})
	if err := logAudit(ctx, deps, toolNameDeleteFamilyCategory, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameDeleteFamilyCategory, err)
	}
	return fmt.Sprintf("ok — category %q deleted", name), nil
}
