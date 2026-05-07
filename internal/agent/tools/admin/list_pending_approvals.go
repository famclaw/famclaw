package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/famclaw/famclaw/internal/agentcore"
)

const toolNameListPendingApprovals = "builtin__list_pending_approvals"

// ListPendingApprovalsDefinition returns the agentcore.Tool definition for the
// list_pending_approvals tool.
func ListPendingApprovalsDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameListPendingApprovals,
		Description: "List all pending approval requests from family members. Returns request ID, user name, topic category, the original query, and when it was requested.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleListPendingApprovals executes the list_pending_approvals tool.
// args is the raw JSON-decoded argument map (empty for this tool).
// Returns a JSON-encoded slice of approval summaries.
func HandleListPendingApprovals(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	if err := logAudit(ctx, deps, toolNameListPendingApprovals, args); err != nil {
		// Non-fatal: log and continue so the read still succeeds.
		fmt.Fprintf(os.Stderr, "[admin] audit log failed: %v\n", err)
	}

	approvals, err := deps.DB.PendingApprovals(ctx)
	if err != nil {
		return "", fmt.Errorf("list pending approvals: %w", err)
	}

	result := make([]map[string]any, 0, len(approvals))
	for _, a := range approvals {
		result = append(result, map[string]any{
			"id":           a.ID,
			"user_name":    a.UserName,
			"category":     a.Category,
			"query_text":   a.QueryText,
			"requested_at": a.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}

	b, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal pending approvals: %w", err)
	}
	return string(b), nil
}
