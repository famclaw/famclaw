package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/famclaw/famclaw/internal/agentcore"
)

const toolNameDenyRequest = "builtin__deny_request"

// DenyRequestDefinition returns the agentcore.Tool definition for the
// deny_request tool.
func DenyRequestDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameDenyRequest,
		Description: "Deny a pending approval request from a family member. Optionally provide a reason that will be recorded with the decision.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"request_id": map[string]any{
					"type":        "string",
					"description": "The approval request UUID",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Optional reason for the denial",
				},
			},
			"required": []string{"request_id"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleDenyRequest executes the deny_request tool.
func HandleDenyRequest(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	requestID, _ := args["request_id"].(string)
	if requestID == "" {
		return "", fmt.Errorf("deny_request: request_id is required")
	}
	reason, _ := args["reason"].(string)

	if err := deps.DB.DecideApprovalWithNote(ctx, requestID, "denied", deps.Actor, reason); err != nil {
		return "", fmt.Errorf("deny_request: %w", err)
	}

	// Fetch full approval row to get requester details for notification.
	approval, err := deps.DB.GetApproval(ctx, requestID)
	if err != nil {
		return "", fmt.Errorf("deny_request: fetch approval: %w", err)
	}

	// Compose denial message including parent name and optional reason.
	var denialMsg string
	if approval != nil {
		if reason != "" {
			denialMsg = fmt.Sprintf(
				"Your request to discuss %s was denied by %s. Reason: %s",
				approval.Category, deps.Actor, reason)
		} else {
			denialMsg = fmt.Sprintf(
				"Your request to discuss %s was denied by %s.",
				approval.Category, deps.Actor)
		}
	}

	// Aggregate per-gateway send results so users with multiple linked
	// accounts (e.g. telegram + discord) don't lose intermediate outcomes
	// from the audit log.
	var notificationParts []string
	if approval != nil && len(deps.Gateways) > 0 {
		accounts, err := deps.DB.ListGatewayAccountsByUser(ctx, approval.UserName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[admin] list gateway accounts failed: %v\n", err)
		} else {
			for _, account := range accounts {
				sender, ok := deps.Gateways[account.Gateway]
				if !ok {
					continue
				}
				if sendErr := sender.Send(account.ExternalID, denialMsg); sendErr != nil {
					notificationParts = append(notificationParts, fmt.Sprintf("failed:%s:%v", account.Gateway, sendErr))
				} else {
					notificationParts = append(notificationParts, fmt.Sprintf("sent:%s", account.Gateway))
				}
			}
		}
	}
	notificationStatus := "skipped:no_sender"
	if len(notificationParts) > 0 {
		notificationStatus = strings.Join(notificationParts, ";")
	}

	userName := ""
	category := ""
	if approval != nil {
		userName = approval.UserName
		category = approval.Category
	}

	auditArgs, _ := json.Marshal(map[string]any{
		"request_id":     requestID,
		"user_name":      userName,
		"category":       category,
		"denial_message": denialMsg,
		"notification":   notificationStatus,
	})
	if err := logAudit(ctx, deps, toolNameDenyRequest, json.RawMessage(auditArgs)); err != nil {
		fmt.Fprintf(os.Stderr, "[admin] audit log failed: %v\n", err)
	}

	result := map[string]any{
		"request_id":     requestID,
		"status":         "denied",
		"decided_by":     deps.Actor,
		"reason":         reason,
		"denial_message": denialMsg,
		"notification":   notificationStatus,
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("deny_request: marshal result: %w", err)
	}
	return string(b), nil
}
