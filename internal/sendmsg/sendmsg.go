// Package sendmsg implements the builtin__send_message tool, which lets the
// model proactively address a family member on a gateway other than the
// one the current message arrived on. The target is resolved against the
// configured family members; their most recent gateway + external_id is
// looked up from the store; delivery goes through the gateway.Sender for
// that platform.
package sendmsg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/store"
)

// ToolName is the name of the send_message tool.
const ToolName = "builtin__send_message"

// SendTimeout bounds a single proactive delivery so a slow gateway cannot
// block the agent loop.
const SendTimeout = 30 * time.Second

// Tool returns the agentcore.Tool definition for builtin__send_message.
// The tool is parent-only at the policy level; the caller must also register
// it only for parent users.
func Tool() agentcore.Tool {
	return agentcore.Tool{
		Name:        ToolName,
		Description: "Send a message to another family member on their platform. The target must be a configured family member; delivery uses their most recent gateway. Only parents may use this tool.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to": map[string]any{
					"type":        "string",
					"description": "Name of the family member to message (must be a configured user).",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Message to send to the target.",
				},
			},
			"required": []string{"to", "message"},
		},
		Source: "builtin",
		Roles:  nil, // all roles at the definition level; restricted by OPA + registration
	}
}

// DB provides the store lookups and audit needed for cross-chat delivery.
// Implemented by *store.DB.
type DB interface {
	MostRecentGatewayAndExternalIDForUser(ctx context.Context, userName string) (gateway, externalID string, err error)
	LogAudit(ctx context.Context, actorName, gateway, toolName string, args []byte) error
	SaveMessage(convID, userName, role, content, category, policyAction, gateway string) error
}

// Handle resolves the target family member's most recent gateway and
// external_id, then delivers the message through the matching gateway.Sender.
//
// The outbound message is recorded in both the audit log (who initiated it,
// the target, and the content) and the target user's conversation history
// so it appears in the web dashboard.
//
// Safety guarantees:
//   - target must be a configured family member (unknown → clear error)
//   - target must have a recorded gateway (no gateway → honest error)
//   - no sender for that gateway → honest error
//   - no broadcast — only the resolved destination is used
func Handle(ctx context.Context, db DB, cfg *config.Config, senderRegistry map[string]gateway.Sender, actor, actorGateway, to, message string) (string, error) {
	if to == "" || message == "" {
		return "", fmt.Errorf("send_message requires both 'to' and 'message'")
	}

	// Only configured family members are addressable.
	if cfg.GetUser(to) == nil {
		return "", fmt.Errorf("%q is not a configured family member", to)
	}

	// Resolve the target's most recent gateway + external_id.
	gatewayName, externalID, err := db.MostRecentGatewayAndExternalIDForUser(ctx, to)
	if err != nil {
		return "", fmt.Errorf("resolving gateway for %s: %w", to, err)
	}
	if gatewayName == "" || externalID == "" {
		return "", fmt.Errorf("%s has not sent any messages yet, so I don't know how to reach them on any gateway", to)
	}

	// Look up the sender for that gateway.
	sender, ok := senderRegistry[gatewayName]
	if !ok {
		return "", fmt.Errorf("no sender available for gateway %q", gatewayName)
	}

	// Deliver through the gateway's sender.
	deadlineCtx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	if err := sender.Send(deadlineCtx, externalID, message); err != nil {
		return "", fmt.Errorf("sending message to %s via %s: %w", to, gatewayName, err)
	}

	// Audit: who sent what to whom, via which gateway.
	auditArgs := map[string]string{
		"to":      to,
		"message": message,
		"gateway": gatewayName,
	}
	if b, jerr := json.Marshal(auditArgs); jerr == nil {
		_ = db.LogAudit(ctx, actor, actorGateway, ToolName, b)
	}

	// Save to the target user's conversation history so the web
	// dashboard shows what the assistant sent.
	convID := store.ConversationID(to, time.Time{}, false, "", time.Now())
	_ = db.SaveMessage(convID, to, "assistant", message, "send_message", "allow", gatewayName)

	return fmt.Sprintf("Message sent to %s via %s", to, gatewayName), nil
}
