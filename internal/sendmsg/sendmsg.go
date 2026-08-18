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
	"errors"
	"fmt"
	"sort"
	"strings"
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
	GatewayAccountsByUserWithLastMsg(ctx context.Context, userName string) ([]store.GatewayAccount, error)
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

	// Only configured family members are addressable. Resolve to the
	// canonical configured name: the model may pass a display name or any
	// case variant ("Julia"), but gateway_accounts and conversations store
	// the lowercase config Name ("julia"). Normalizing here keeps the
	// destination lookup, audit trail, and conversation history consistent.
	canonicalTo, ok := cfg.CanonicalName(to)
	if !ok {
		return "", fmt.Errorf("%q is not a configured family member", to)
	}
	to = canonicalTo

	// Resolve a gateway that can actually be INITIATED for a proactive
	// delivery to `to`. Being linked on a gateway (a row in gateway_accounts)
	// is necessary but not sufficient: a bot cannot start a Telegram
	// conversation with a user who has never messaged it. ResolveDestination
	// encodes those platform rules and returns a clear, actionable error when
	// no linked gateway can be initiated (instead of failing opaquely inside
	// the platform Send call).
	gatewayName, externalID, err := ResolveDestination(ctx, db, to)
	if err != nil {
		return "", err
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

// ResolveDestination chooses a linked gateway that can actually be INITIATED
// for a proactive delivery to to, returning its name and platform external_id.
//
// A family member being "linked" (a row in gateway_accounts) is necessary but
// not sufficient for proactive delivery: not every linked gateway can be
// started on demand. The platform rules are:
//
//   - discord: a bot can open a DM to a shared-guild member who has never
//     messaged it (subject to their privacy settings), so any linked Discord
//     account is initiable.
//   - telegram: a bot CANNOT start a conversation; the user must have sent the
//     bot at least one message. Once that route exists it stays open
//     permanently. So a linked Telegram account is initiable only when our
//     messages table shows prior inbound activity on that gateway.
//
// Resolution prefers an initiable gateway; among several initiable ones it
// keeps the existing most-recent-message preference (NULL last-message time
// sorts oldest, so a never-messaged-but-always-initiable Discord loses to a
// used Telegram).
//
// When no linked gateway can be initiated, the returned error names the person,
// lists the gateways they ARE linked on, explains why each cannot be
// initiated, and says what would unblock it. This replaces the opaque
// "X has not sent any messages yet" failure with something actionable.
//
// To add a third gateway, extend canInitiate / cannotInitiateReason below.
func ResolveDestination(ctx context.Context, db DB, to string) (gateway, externalID string, err error) {
	linked, err := db.GatewayAccountsByUserWithLastMsg(ctx, to)
	if err != nil {
		return "", "", fmt.Errorf("resolving reachable gateway for %s: %w", to, err)
	}
	if len(linked) == 0 {
		return "", "", fmt.Errorf("%s has no linked gateway account to send through, so I don't know how to reach them on any gateway", to)
	}

	// Partition into initiable / not. hasMsg is derived from lastMsgAt, which
	// is the messages-table lookup that decides Telegram iniability.
	initable := make([]candidate, 0, len(linked))
	not := make([]candidate, 0, len(linked))
	for _, acc := range linked {
		hasMsg := acc.LastMsgAt != nil
		c := candidate{gw: acc.Gateway, ext: acc.ExternalID, lastMsg: acc.LastMsgAt}
		if canInitiate(acc.Gateway, hasMsg) {
			initable = append(initable, c)
		} else {
			not = append(not, c)
		}
	}

	if len(initable) == 0 {
		return "", "", unresolvableError(to, not)
	}

	// Prefer most-recent; NULL lastMsg sorts oldest (stable tiebreak on name).
	sort.Slice(initable, func(i, j int) bool {
		return candidateIsNewer(initable[i], initable[j])
	})
	best := initable[0]
	return best.gw, best.ext, nil
}

// candidateIsNewer reports whether a is more recently messaged than b, for
// preferring the gateway the user last used. NULL lastMsg is treated as the
// oldest possible time.
func candidateIsNewer(a, b candidate) bool {
	if a.lastMsg == nil && b.lastMsg == nil {
		return a.gw < b.gw // stable tiebreak
	}
	if a.lastMsg == nil {
		return false // a is older
	}
	if b.lastMsg == nil {
		return true // b is older
	}
	if !a.lastMsg.Equal(*b.lastMsg) {
		return a.lastMsg.After(*b.lastMsg)
	}
	return a.gw < b.gw // stable tiebreak
}

// candidate is a linked gateway being evaluated for proactive delivery.
type candidate struct {
	gw      string
	ext     string
	lastMsg *time.Time
}

// canInitiate reports whether a bot can proactively open a conversation on
// gateway to a user. hasMsg is whether the user has ever messaged the bot on
// that gateway (the messages-table signal).
func canInitiate(gateway string, hasMsg bool) bool {
	switch gateway {
	case "discord":
		return true
	case "telegram":
		return hasMsg
	default:
		// Unknown / placeholder gateways (e.g. whatsapp stubs) cannot be
		// initiated until a driver with a start-chat capability is added.
		return false
	}
}

// cannotInitiateReason explains, for a non-initiable gateway, why a bot cannot
// start a conversation there. (discord never reaches this for a non-initiable
// gateway, so its case documents the invariant.)
func cannotInitiateReason(gateway string) string {
	switch gateway {
	case "telegram":
		return "Telegram does not let a bot start a conversation — they must send the bot one message first"
	case "discord":
		return "always initiable"
	default:
		return "no bot can start a conversation on this gateway"
	}
}

// unresolvableError builds the actionable "I can't reach X" message for the
// case where the user is linked on one or more gateways but none can be
// initiated right now.
func unresolvableError(to string, not []candidate) error {
	reasons := make([]string, 0, len(not))
	for _, c := range not {
		reasons = append(reasons, fmt.Sprintf("%s — %s", c.gw, cannotInitiateReason(c.gw)))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "I can't reach %s with a proactive message right now. ", to)
	fmt.Fprintf(&b, "%s is linked on %s, and a bot cannot start a conversation on any of them at the moment. ",
		to, strings.Join(reasons, ", "))
	if hasGateway(not, "telegram") {
		fmt.Fprintf(&b, "To start the Telegram route, ask %s to send one message to the FamClaw bot on Telegram — after that the route stays open permanently. ", to)
	}
	fmt.Fprintf(&b, "Linking %s on Discord would also let me open a DM there directly, since Discord allows bots to start DMs with shared-guild members. ", to)
	return errors.New(b.String())
}

func hasGateway(cands []candidate, gateway string) bool {
	for _, c := range cands {
		if c.gw == gateway {
			return true
		}
	}
	return false
}
