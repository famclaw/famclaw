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

	// SharedPolicyOwner is the single shared lock guarding this config's
	// WebFetch host policy slices (URLAllowlist / URLDenylist): one
	// instance per *config.Config, shared by every Agent and every handler
	// built from that config (#423 round 18 — replaces the per-Agent value
	// mutex urlDenylistMu, which could never exclude across Agents). The
	// denylist_* and allowlist_* handlers take it (RLock / Lock) so their
	// read-modify-writes never race hostDecision's decision-time snapshots
	// on ANY Agent, and never tear the slice headers.
	//
	// It is REQUIRED for any entry point that can run concurrently with the
	// agent's hostDecision path: when nil, policyOwner() returns
	// ErrPolicyOwnerUnwired and every web-policy handler fails closed
	// instead of touching the lists, so a misconfigured entry point
	// surfaces the error at the call site rather than degrading to an
	// unsynchronized read-modify-write.
	//
	// NOTE: the field is a pointer to the config's shared owner — never a
	// value sync.RWMutex: a value field would be copied into each Deps and
	// could not be shared across Agents.
	SharedPolicyOwner *config.WebFetchPolicyOwner

	// SharedConfig is the shared configuration manager for atomic
	// save/reload of the config file (transactional: content-token
	// precondition, temp-file + rename commit, operator-conflict rollback).
	// The denylist/allowlist add/remove tools call its SaveConfig inside
	// the policy-owner (M) span; a nil value means the entry point is
	// memory-only (no on-disk persistence), and persist is then skipped.
	SharedConfig *SharedConfigManager
}

// ErrPolicyOwnerUnwired is returned by policyOwner() when
// Deps.SharedPolicyOwner is nil. All web-policy handlers (denylist AND
// allowlist) fail closed on it: proceeding without the config's shared
// owner would let their read-modify-write of Cfg.Tools.WebFetch lists race
// hostDecision's decision-time snapshot on any Agent, so the
// misconfiguration is returned to the caller instead of being logged and
// continued.
var ErrPolicyOwnerUnwired = errors.New("admin web policy: Deps.SharedPolicyOwner is not wired to the config's shared WebFetchPolicyOwner; refusing to touch the URL policy unsynchronized")

// policyOwner returns the config's shared WebFetch policy owner for a
// web-policy handler. When Deps.SharedPolicyOwner is wired it returns that
// instance so the handler's read-modify-write of Cfg.Tools.WebFetch lists
// never races hostDecision's decision-time snapshot on any Agent. When
// unwired it logs a prominent error and returns ErrPolicyOwnerUnwired, so
// the handler fails closed at the call site rather than degrading to an
// unsynchronized read-modify-write.
func (d Deps) policyOwner() (*config.WebFetchPolicyOwner, error) {
	if d.SharedPolicyOwner == nil {
		log.Printf("[admin] ERROR: web policy handler invoked with SharedPolicyOwner unset — failing closed. Every entry point that can run concurrently with the agent's hostDecision path must wire Deps.SharedPolicyOwner to the config's shared owner (cfg.WebFetchPolicyOwner()).")
		return nil, ErrPolicyOwnerUnwired
	}
	return d.SharedPolicyOwner, nil
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
	if deps.DB == nil {
		return nil
	}
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
