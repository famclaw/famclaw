package reminder

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// ToolName is the name of the reminder tool.
const ToolName = "builtin__add_reminder"

// Tool returns the agentcore.Tool definition for the add_reminder tool.
func Tool() agentcore.Tool {
	return agentcore.Tool{
		Name:        ToolName,
		Description: "Set a reminder for yourself or another family member. Specify when (relative like 'in 2 hours', 'tomorrow 9am', or shorthand '30m') and the message to be reminded of.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"when": map[string]any{
					"type":        "string",
					"description": "When to remind (e.g., 'in 2 hours', 'tomorrow 9am', '30m', 'at 14:30', 'monday 10:00')",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "What to be reminded about",
				},
				"for_user": map[string]any{
					"type":        "string",
					"description": "Optional: set reminder for another family member (parent only). Defaults to the current user.",
				},
			},
			"required": []string{"when", "message"},
		},
		Source: "builtin",
		Roles:  []string{"parent", "child"},
	}
}

// HandleAddReminder creates a reminder and stores it in the database.
// The agent's makeBuiltinHandler should call this with the appropriate deps.
func HandleAddReminder(ctx context.Context, db *store.DB, user *config.UserConfig, gateway, externalID, groupID string, isGroup bool, when, message, forUser string) (string, error) {
	// Parse the time
	now := time.Now().UTC()
	dueAt, err := ParseTime(when, now)
	if err != nil {
		return "", fmt.Errorf("invalid time: %w", err)
	}

	// Validate due time is in the future
	if dueAt.Before(now) || dueAt.Equal(now) {
		return "", fmt.Errorf("reminder time must be in the future")
	}

	// Determine target user
	targetUser := user.Name
	if forUser != "" {
		if user.Role != "parent" {
			return "", fmt.Errorf("only parents can set reminders for other users")
		}
		targetUser = forUser
	}

	reminder := &store.Reminder{
		UserName:   targetUser,
		Gateway:    gateway,
		ExternalID: externalID,
		GroupID:    groupID,
		IsGroup:    isGroup,
		Message:    message,
		DueAt:      dueAt,
		CreatedAt:  now,
		Dispatched: false,
	}

	if err := db.CreateReminder(ctx, reminder); err != nil {
		return "", fmt.Errorf("creating reminder: %w", err)
	}

	result := map[string]any{
		"reminder_id": reminder.ID,
		"due_at":      dueAt.Format(time.RFC3339),
		"message":     message,
		"for_user":    targetUser,
		"status":      "scheduled",
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}