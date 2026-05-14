package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/familystate"
)

const toolNameAddFamilyCategory = "builtin__add_family_category"

// categoryNameRE constrains custom category names to lower-case [a-z0-9_]+.
// Built-in categories already follow this pattern; the constraint also
// keeps category names safe for embedding in prompt labels.
var categoryNameRE = regexp.MustCompile(`^[a-z0-9_]+$`)

// AddFamilyCategoryDefinition returns the parent-only tool that creates
// a custom category. always_inject categories appear in every system
// prompt — use sparingly to avoid token bloat.
func AddFamilyCategoryDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name: toolNameAddFamilyCategory,
		Description: "Create a new family-fact category (parent-only). " +
			"Use to organize family-specific facts that don't fit the built-in categories (allergies, dietary_restrictions, important_dates, pets). " +
			"Example: add_family_category(name=\"movie_night\", description=\"recurring family activities\", always_inject=false)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":          map[string]any{"type": "string", "description": "Lower-case category name, [a-z0-9_]+, ≤ 32 chars."},
				"description":   map[string]any{"type": "string", "description": "Human-readable purpose of the category."},
				"always_inject": map[string]any{"type": "boolean", "description": "If true, facts in this category appear in every system prompt. Default false."},
			},
			"required": []string{"name", "description"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleAddFamilyCategory dispatches builtin__add_family_category.
func HandleAddFamilyCategory(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	if deps.FamilyState == nil {
		return "", fmt.Errorf("add_family_category: family state is not configured")
	}
	name, _ := args["name"].(string)
	desc, _ := args["description"].(string)
	always, _ := args["always_inject"].(bool)

	if name == "" || desc == "" {
		return "", fmt.Errorf("add_family_category: name and description are required")
	}
	if len(name) > 32 || !categoryNameRE.MatchString(name) {
		return fmt.Sprintf("invalid category name %q — must be [a-z0-9_]+ and ≤ 32 chars", name), nil
	}
	if len(desc) > 256 {
		return "description too long (max 256 chars)", nil
	}

	cat := familystate.Category{Name: name, Description: desc, AlwaysInject: always}
	if err := deps.FamilyState.UpsertCategory(ctx, &cat); err != nil {
		if errors.Is(err, familystate.ErrBuiltinCategory) {
			return fmt.Sprintf("category %q is built-in and cannot be modified via this tool", name), nil
		}
		return "", fmt.Errorf("add_family_category: %w", err)
	}
	auditArgs, _ := json.Marshal(map[string]any{"name": name, "always_inject": always})
	if err := logAudit(ctx, deps, toolNameAddFamilyCategory, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameAddFamilyCategory, err)
	}
	return fmt.Sprintf("ok — category %q ready", name), nil
}
