package usermemory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/famclaw/famclaw/internal/agentcore"
)

// Tool names for the builtin user memory tools.
const (
	toolNameRemember = "builtin__remember_user_memory"
	toolNameRecall   = "builtin__recall_user_memory"
	toolNameForget   = "builtin__forget_user_memory"
)

// RememberDefinition returns the tool definition for remembering a user memory.
func RememberDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameRemember,
		Description: "Remember a fact about the current user (preferences, ongoing context, things they told you to remember). Scoped to this user only. Example: remember_user_memory(category=\"preferences\", label=\"coffee\", value=\"black, no sugar\")",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{"type": "string", "description": "Category name, e.g. 'preferences', 'projects', 'reminders'."},
				"label":    map[string]any{"type": "string", "description": "Short label for the memory: 'coffee', 'project_phoenix', 'mom_birthday'."},
				"value":    map[string]any{"type": "string", "description": "The actual memory content to store."},
			},
			"required": []string{"category", "label", "value"},
		},
		Source: "builtin",
		Roles:  []string{"parent", "child", "age_8_12", "age_13_17", "under_8"},
	}
}

// RecallDefinition returns the tool definition for recalling user memories.
func RecallDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameRecall,
		Description: "Recall memories for the current user. Optionally filter by category. Returns all memories if category omitted.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{"type": "string", "description": "Optional category to filter by (e.g. 'preferences')."},
			},
		},
		Source: "builtin",
		Roles:  []string{"parent", "child", "age_8_12", "age_13_17", "under_8"},
	}
}

// ForgetDefinition returns the tool definition for forgetting a user memory.
func ForgetDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameForget,
		Description: "Forget a specific user memory by category and label. Use when the user asks you to forget something or when a memory is outdated.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{"type": "string", "description": "Category of the memory to forget."},
				"label":    map[string]any{"type": "string", "description": "Label of the memory to forget."},
			},
			"required": []string{"category", "label"},
		},
		Source: "builtin",
		Roles:  []string{"parent", "child", "age_8_12", "age_13_17", "under_8"},
	}
}

// HandleRemember dispatches the remember_user_memory tool.
func HandleRemember(ctx context.Context, store *Store, userName, category, label, value string) (string, error) {
	if category == "" || label == "" || value == "" {
		return "", fmt.Errorf("category, label, and value are all required")
	}
	if len(label) > 64 {
		return "label too long (max 64 chars)", nil
	}
	if len(value) > 2048 {
		return "value too long (max 2048 chars)", nil
	}

	m := &Memory{
		UserName: userName,
		Category: category,
		Label:    label,
		Value:    value,
	}
	if err := store.UpsertMemory(ctx, m); err != nil {
		return "", fmt.Errorf("remember memory: %w", err)
	}
	return fmt.Sprintf("ok — remembered #%d", m.ID), nil
}

// HandleRecall dispatches the recall_user_memory tool.
func HandleRecall(ctx context.Context, store *Store, userName, category string) (string, error) {
	memories, err := store.ListMemories(ctx, userName, category)
	if err != nil {
		return "", fmt.Errorf("recall memories: %w", err)
	}
	if len(memories) == 0 {
		if category != "" {
			return fmt.Sprintf("No memories in category %q.", category), nil
		}
		return "No memories stored yet.", nil
	}

	var b strings.Builder
	if category != "" {
		fmt.Fprintf(&b, "Memories in %q:\n", category)
	} else {
		b.WriteString("All memories:\n")
	}

	currentCat := ""
	for _, m := range memories {
		if m.Category != currentCat {
			if currentCat != "" {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "%s:\n", categoryDisplayLabel(m.Category))
			currentCat = m.Category
		}
		fmt.Fprintf(&b, "  - %s: %s\n", m.Label, m.Value)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// HandleForget dispatches the forget_user_memory tool.
func HandleForget(ctx context.Context, store *Store, userName, category, label string) (string, error) {
	if category == "" || label == "" {
		return "", fmt.Errorf("category and label are required")
	}
	err := store.DeleteMemoryByKey(ctx, userName, category, label)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Sprintf("No memory found with category %q and label %q.", category, label), nil
		}
		return "", fmt.Errorf("forget memory: %w", err)
	}
	return "ok — forgotten", nil
}
