// Package admin provides built-in admin tool handlers (read-only and
// mutating) for parent users. Each tool is injected into the agent's
// BuiltinTools slice and dispatched by makeBuiltinHandler() in agent.go.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/familystate"
	"github.com/famclaw/famclaw/internal/skillbridge"
	"github.com/famclaw/famclaw/internal/store"
)

// GatewaySender is a minimal interface for dispatching a message to a chat user.
// Implementations are provided by each gateway bot (telegram, discord).
// The chatID is the gateway-specific external identifier (e.g. Telegram chat ID).
type GatewaySender interface {
	Send(chatID string, msg string) error
}

// Deps holds the dependencies injected into every admin tool handler.
// It is constructed inline in agent.go's makeBuiltinHandler() switch cases.
type Deps struct {
	DB          *store.DB
	Cfg         *config.Config
	Actor       string                   // the user name of the parent invoking the tool
	Gateway     string                   // the gateway they're calling from (telegram, discord, web, etc.)
	Gateways    map[string]GatewaySender // keyed by gateway name; may be nil
	FamilyState *familystate.Store       // Phase 3.3 — required for family_* admin tools; nil disables them

	// MCP is the pool that manages MCP server lifecycle. When non-nil,
	// mcp_add / mcp_list tools are enabled. The agent injects its
	// *mcp.Pool which satisfies this interface.
	MCP MCPManager

	// ConfigPath is the on-disk path to config.yaml, used by mcp_add
	// to persist new MCP server entries so they survive restarts.
	ConfigPath string
	Scanner    skillbridge.Scanner

	// URLDenylistMu is the agent's denylist lock, shared with the
	// decision path (Agent.urlDenylistMu in internal/agent). The
	// denylist_list / denylist_add / denylist_remove handlers take it
	// (RLock / Lock) so their read-modify-writes of
	// Cfg.Tools.WebFetch.URLDenylist never race hostDecision's
	// decision-time slice reads.
	//
	// It is REQUIRED for any entry point that can run concurrently with
	// the agent: when nil, denylistMu() returns ErrDenylistMuUnwired and
	// every denylist handler fails closed instead of touching the list,
	// so a misconfigured entry point surfaces the error at the call site
	// rather than degrading to a partial lock that does not serialize
	// against the agent's decision path (review finding: global no-op
	// mutex).
	//
	// NOTE: a value sync.RWMutex is deliberately NOT used here: the
	// field must share the agent's lock instance (&a.urlDenylistMu),
	// and a value field would be copied into each Deps, defeating the
	// synchronization.
	URLDenylistMu *sync.RWMutex
}

// ErrDenylistMuUnwired is returned by denylistMu() when
// Deps.URLDenylistMu is nil. All denylist handlers fail closed on it:
// proceeding without the agent's lock would let their read-modify-write
// of Cfg.Tools.WebFetch.URLDenylist race hostDecision's decision-time
// slice reads, so the misconfiguration is returned to the caller instead
// of being logged and continued.
var ErrDenylistMuUnwired = errors.New("admin denylist: Deps.URLDenylistMu is not wired to the agent's urlDenylistMu; refusing to touch the denylist unsynchronized")

// denylistMu returns the shared denylist lock for a denylist handler.
// When Deps.URLDenylistMu is wired it returns that instance so the
// handler's read-modify-write of Cfg.Tools.WebFetch.URLDenylist never
// races hostDecision's decision-time slice reads. When unwired it logs a
// prominent error and returns ErrDenylistMuUnwired, so the handler
// fails closed at the call site rather than degrading to a no-op lock
// that would not serialize against the agent's decision path.
func (d Deps) denylistMu() (*sync.RWMutex, error) {
	if d.URLDenylistMu == nil {
		log.Printf("[admin] ERROR: denylist handler invoked with URLDenylistMu unset — failing closed. Every entry point that can run concurrently with the agent's hostDecision path must wire Deps.URLDenylistMu to the agent's urlDenylistMu.")
		return nil, ErrDenylistMuUnwired
	}
	return d.URLDenylistMu, nil
}

// MCPManager abstracts MCP server lifecycle management needed by the
// mcp_add / mcp_list admin tools. The agent's *mcp.Pool satisfies this.
type MCPManager interface {
	// ReloadConfig synchronizes the pool's MCP servers with the given
	// config. Called after mcp_add mutates the config in-memory.
	ReloadConfig(cfg *config.Config) map[string]error
}

// isKnownSubject reports whether subject is one of the configured user
// names or the literal "family". Shared by every family_* admin tool
// that accepts a subject argument.
func isKnownSubject(cfg *config.Config, subject string) bool {
	if subject == "family" {
		return true
	}
	if cfg == nil {
		return false
	}
	for _, u := range cfg.Users {
		if u.Name == subject {
			return true
		}
	}
	return false
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
		MCPListDefinition(),
		AllowlistListDefinition(),
		DenylistListDefinition(),
	}
}

// AllMutatingDefinitions returns agentcore.Tool definitions for all mutating
// admin tools (approve, deny, set role, link account, family_* mutations).
func AllMutatingDefinitions() []agentcore.Tool {
	return []agentcore.Tool{
		ApproveRequestDefinition(),
		DenyRequestDefinition(),
		SetUserRoleDefinition(),
		LinkAccountDefinition(),
		// Phase 3.3:
		SetFamilyFactDefinition(),
		DeleteFamilyFactDefinition(),
		AddFamilyCategoryDefinition(),
		DeleteFamilyCategoryDefinition(),
		MCPAddDefinition(),
		MCPRemoveDefinition(),
		AllowlistAddDefinition(),
		AllowlistRemoveDefinition(),
		DenylistAddDefinition(),
		DenylistRemoveDefinition(),
	}
}

// AllDefinitions returns the full set of admin tool definitions (read-only +
// mutating). Wire these into AgentDeps.BuiltinTools for parent agents that
// need full admin access.
func AllDefinitions() []agentcore.Tool {
	return append(AllReadOnlyDefinitions(), AllMutatingDefinitions()...)
}
