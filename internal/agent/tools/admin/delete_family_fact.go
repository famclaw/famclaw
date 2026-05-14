package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/famclaw/famclaw/internal/agentcore"
)

const toolNameDeleteFamilyFact = "builtin__delete_family_fact"

// DeleteFamilyFactDefinition returns the parent-only tool that removes one
// fact row by numeric id. Idempotent — a missing id is not an error.
func DeleteFamilyFactDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameDeleteFamilyFact,
		Description: "Delete one family fact by its numeric id (parent-only). Use after get_family_state surfaces the id you want to remove.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "integer", "description": "The fact's numeric id."},
			},
			"required": []string{"id"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleDeleteFamilyFact dispatches builtin__delete_family_fact.
func HandleDeleteFamilyFact(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	if deps.FamilyState == nil {
		return "", fmt.Errorf("delete_family_fact: family state is not configured")
	}
	idF, ok := args["id"].(float64)
	if !ok {
		return "", fmt.Errorf("delete_family_fact: id must be a number")
	}
	id := int64(idF)
	if id <= 0 {
		return "", fmt.Errorf("delete_family_fact: id must be positive")
	}
	if err := deps.FamilyState.DeleteFact(ctx, id); err != nil {
		return "", fmt.Errorf("delete_family_fact: %w", err)
	}
	auditArgs, _ := json.Marshal(map[string]any{"id": id})
	if err := logAudit(ctx, deps, toolNameDeleteFamilyFact, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameDeleteFamilyFact, err)
	}
	return fmt.Sprintf("ok — fact #%d deleted (or did not exist)", id), nil
}
