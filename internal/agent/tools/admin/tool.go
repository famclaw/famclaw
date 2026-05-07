// Package admin provides built-in admin tool handlers (read-only and
// mutating) for parent users. Each tool is injected into the agent's
// BuiltinTools slice and dispatched by makeBuiltinHandler() in agent.go.
package admin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// GatewaySender is a minimal interface for dispatching a message to a chat user.
// Implementations are provided by each gateway bot (telegram, discord, whatsapp).
// The chatID is the gateway-specific external identifier (e.g. Telegram chat ID).
type GatewaySender interface {
	Send(chatID string, msg string) error
}

// Deps holds the dependencies injected into every admin tool handler.
// It is constructed inline in agent.go's makeBuiltinHandler() switch cases.
type Deps struct {
	DB       *store.DB
	Cfg      *config.Config
	Actor    string // the user name of the parent invoking the tool
	Gateway  string // the gateway they're calling from (telegram, discord, web, etc.)
	Gateways map[string]GatewaySender // keyed by gateway name; may be nil
}

// logAudit writes an audit record. args must be JSON-marshalable.
func logAudit(ctx context.Context, deps Deps, toolName string, args any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("marshal audit args: %w", err)
	}
	return deps.DB.LogAudit(ctx, deps.Actor, deps.Gateway, toolName, b)
}

// AllReadOnlyDefinitions returns agentcore.Tool definitions for all read-only
// admin tools. Wire these into AgentDeps.BuiltinTools for parent agents.
func AllReadOnlyDefinitions() []agentcore.Tool {
	return []agentcore.Tool{
		ListPendingApprovalsDefinition(),
		ListUsersDefinition(),
		ListUnknownAccountsDefinition(),
	}
}

// AllMutatingDefinitions returns agentcore.Tool definitions for all mutating
// admin tools (approve, deny, set role, link account).
func AllMutatingDefinitions() []agentcore.Tool {
	return []agentcore.Tool{
		ApproveRequestDefinition(),
		DenyRequestDefinition(),
		SetUserRoleDefinition(),
		LinkAccountDefinition(),
	}
}

// AllDefinitions returns the full set of admin tool definitions (read-only +
// mutating). Wire these into AgentDeps.BuiltinTools for parent agents that
// need full admin access.
func AllDefinitions() []agentcore.Tool {
	return append(AllReadOnlyDefinitions(), AllMutatingDefinitions()...)
}
