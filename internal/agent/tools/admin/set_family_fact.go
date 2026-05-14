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

const toolNameSetFamilyFact = "builtin__set_family_fact"

// SetFamilyFactDefinition returns the parent-only tool that creates or
// updates one row in family_facts. The UNIQUE(category, subject, label)
// key determines upsert vs insert — re-calling with the same triple
// updates value/recurrence.
func SetFamilyFactDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name: toolNameSetFamilyFact,
		Description: "Add or update a family fact (parent-only). " +
			"Use to record allergies, dietary restrictions, important dates, pet info, or anything in a custom category. " +
			"Example: set_family_fact(category=\"allergies\", subject=\"teo\", label=\"peanuts\", value=\"severe — EpiPen in Mom's purse\")",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{"type": "string", "description": "Category name, e.g. 'allergies'."},
				"subject":  map[string]any{"type": "string", "description": "Username from config or the literal 'family'."},
				"label":    map[string]any{"type": "string", "description": "The specific item: 'peanuts', 'Stella', 'Saturday'."},
				"value":    map[string]any{"type": "string", "description": "Free-form details about the labelled item."},
			},
			"required": []string{"category", "subject", "label", "value"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleSetFamilyFact dispatches builtin__set_family_fact.
func HandleSetFamilyFact(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	if deps.FamilyState == nil {
		return "", fmt.Errorf("set_family_fact: family state is not configured")
	}
	category, _ := args["category"].(string)
	subject, _ := args["subject"].(string)
	label, _ := args["label"].(string)
	value, _ := args["value"].(string)

	if category == "" || subject == "" || label == "" || value == "" {
		return "", fmt.Errorf("set_family_fact: category, subject, label, and value are all required")
	}
	if len(label) > 64 {
		return "label too long (max 64 chars)", nil
	}
	if len(value) > 512 {
		return "value too long (max 512 chars)", nil
	}

	if !isKnownSubject(deps.Cfg, subject) {
		return fmt.Sprintf("subject %q is not a family member", subject), nil
	}

	f := familystate.Fact{
		Category:  category,
		Subject:   subject,
		Label:     label,
		Value:     value,
		CreatedBy: deps.Actor,
	}
	if err := deps.FamilyState.UpsertFact(ctx, &f); err != nil {
		if errors.Is(err, familystate.ErrUnknownCategory) {
			return fmt.Sprintf("unknown category %q — a parent can create it via add_family_category", category), nil
		}
		return "", fmt.Errorf("set_family_fact: %w", err)
	}

	auditArgs, _ := json.Marshal(map[string]any{
		"category": category, "subject": subject, "label": label, "value": value, "id": f.ID,
	})
	if err := logAudit(ctx, deps, toolNameSetFamilyFact, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameSetFamilyFact, err)
	}
	return fmt.Sprintf("ok — fact #%d", f.ID), nil
}
