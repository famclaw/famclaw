package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/famclaw/famclaw/internal/agentcore"
)

const toolNameApproveRequest = "builtin__approve_request"

// ApproveRequestDefinition returns the agentcore.Tool definition for the
// approve_request tool.
func ApproveRequestDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameApproveRequest,
		Description: "Approve a pending approval request from a family member. Marks the request as approved so the member may proceed with the restricted topic.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"request_id": map[string]any{
					"type":        "string",
					"description": "The approval request UUID",
				},
			},
			"required": []string{"request_id"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleApproveRequest executes the approve_request tool.
func HandleApproveRequest(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	requestID, _ := args["request_id"].(string)
	if requestID == "" {
		return "", fmt.Errorf("approve_request: request_id is required")
	}

	if err := deps.DB.DecideApprovalWithNote(ctx, requestID, "approved", deps.Actor, ""); err != nil {
		return "", fmt.Errorf("approve_request: %w", err)
	}

	// Fetch full approval row to get requester details for notification.
	// Best-effort: the decision row is already committed above, so a fetch
	// failure must NOT abort the tool — it would skip the audit entry
	// (returned before logAudit) while leaving the DB mutated. Degrade to
	// approval=nil and continue; the audit/result still records the decision
	// minus the requester details.
	approval, err := deps.DB.GetApproval(ctx, requestID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[admin] approve_request: fetch approval %s after decide: %v\n", requestID, err)
		approval = nil
	}

	// Aggregate per-gateway send results so users with multiple linked
	// accounts (e.g. telegram + discord) don't lose intermediate outcomes
	// from the audit log.
	var notificationParts []string
	if approval != nil && len(deps.Gateways) > 0 {
		accounts, err := deps.DB.ListGatewayAccountsByUser(ctx, approval.UserName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[admin] list gateway accounts failed: %v\n", err)
			notificationParts = append(notificationParts, fmt.Sprintf("failed:list_accounts:%v", err))
		} else {
			msg := fmt.Sprintf(
				"Your request to discuss %s has been approved by %s. You may now re-send your message.",
				approval.Category, deps.Actor)
			for _, account := range accounts {
				sender, ok := deps.Gateways[account.Gateway]
				if !ok {
					continue
				}
				if sendErr := sender.Send(account.ExternalID, msg); sendErr != nil {
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
		"request_id":   requestID,
		"user_name":    userName,
		"category":     category,
		"notification": notificationStatus,
	})
	if err := logAudit(ctx, deps, toolNameApproveRequest, json.RawMessage(auditArgs)); err != nil {
		fmt.Fprintf(os.Stderr, "[admin] audit log failed: %v\n", err)
	}

	result := map[string]any{
		"request_id":   requestID,
		"status":       "approved",
		"decided_by":   deps.Actor,
		"notification": notificationStatus,
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("approve_request: marshal result: %w", err)
	}
	return string(b), nil
}
