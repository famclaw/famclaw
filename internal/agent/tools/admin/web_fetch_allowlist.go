package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/config"
	"gopkg.in/yaml.v3"
)

const (
	toolNameAllowlistList   = "builtin__allowlist_list"
	toolNameAllowlistAdd    = "builtin__allowlist_add"
	toolNameAllowlistRemove = "builtin__allowlist_remove"
)

// AllowlistListDefinition returns the parent-only tool that lists all hosts
// in the web_fetch URL allowlist.
func AllowlistListDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name: toolNameAllowlistList,
		Description: "List all hosts in the web_fetch URL allowlist (parent-only). " +
			"Shows every host the assistant is permitted to fetch via web_fetch.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []string{},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleAllowlistList dispatches builtin__allowlist_list.
func HandleAllowlistList(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	// Snapshot under the shared owner's read lock (review F1: the allowlist
	// handlers previously took NO lock at all). The copy excludes every
	// concurrent allowlist/denylist writer and the settings publish span.
	// policyOwner() fails closed when the owner is unwired
	// (ErrPolicyOwnerUnwired).
	owner, err := deps.policyOwner()
	if err != nil {
		return "", err
	}
	owner.RLock()
	hosts := make([]string, len(deps.Cfg.Tools.WebFetch.URLAllowlist))
	copy(hosts, deps.Cfg.Tools.WebFetch.URLAllowlist)
	owner.RUnlock()

	if len(hosts) == 0 {
		return "web_fetch URL allowlist is empty — no hosts are permitted", nil
	}
	var lines []string
	for _, h := range hosts {
		lines = append(lines, "  \u2022 "+h)
	}
	return fmt.Sprintf("web_fetch URL allowlist (%d hosts):\n%s", len(hosts), strings.Join(lines, "\n")), nil
}

// AllowlistAddDefinition returns the parent-only tool that adds a host to the
// web_fetch URL allowlist.
func AllowlistAddDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name: toolNameAllowlistAdd,
		Description: "Add a host to the web_fetch URL allowlist (parent-only). " +
			"Example: allowlist_add(host=\"example.com\")",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host": map[string]any{
					"type":        "string",
					"description": "The hostname to allow (e.g. 'example.com' or 'api.service.io').",
				},
			},
			"required": []string{"host"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleAllowlistAdd dispatches builtin__allowlist_add.
func HandleAllowlistAdd(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	host, _ := args["host"].(string)
	if host == "" {
		return "", fmt.Errorf("allowlist_add requires a 'host' argument (e.g. allowlist_add(host=\"example.com\"))")
	}

	// Canonicalize: accepts bare host, full URL, or URL with scheme/port.
	// Rejects anything that wouldn't match the web_fetch host gate.
	host, err := config.NormalizeAllowlistHost(host)
	if err != nil {
		return "", fmt.Errorf("allowlist_add: %w", err)
	}

	// #423 round 18 (review F1 + S2): the config's ONE shared policy owner
	// is taken with no other lock held and spans the whole
	// read-modify-write + persist. Before this round the allowlist handlers
	// took NO lock at all and rewrote Cfg.Tools.WebFetch.URLAllowlist
	// unsynchronized against hostDecision's decision-time snapshot — a data
	// race. policyOwner() fails closed when the owner is unwired
	// (ErrPolicyOwnerUnwired).
	owner, err := deps.policyOwner()
	if err != nil {
		return "", err
	}
	owner.Lock()
	defer owner.Unlock()

	wf := &deps.Cfg.Tools.WebFetch
	for _, existing := range wf.URLAllowlist {
		if strings.EqualFold(existing, host) {
			return fmt.Sprintf("%q is already in the allowlist", existing), nil
		}
	}

	// Append to allowlist in-memory.
	wf.URLAllowlist = append(wf.URLAllowlist, host)

	// Persist to disk immediately.
	if deps.ConfigPath != "" {
		pErr := writeConfigInline(deps.ConfigPath, deps.Cfg)
		if pErr != nil {
			log.Printf("[admin] failed to persist allowlist_add: %v", pErr)
			return fmt.Sprintf("added %q to in-memory allowlist, but failed to persist to disk: %v", host, pErr), nil
		}
	}

	auditArgs, _ := json.Marshal(map[string]any{"host": host})
	if err := logAudit(ctx, deps, toolNameAllowlistAdd, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameAllowlistAdd, err)
	}
	return fmt.Sprintf("added %q to web_fetch URL allowlist (%d total)", host, len(wf.URLAllowlist)), nil
}

// AllowlistRemoveDefinition returns the parent-only tool that removes a host
// from the web_fetch URL allowlist.
func AllowlistRemoveDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name: toolNameAllowlistRemove,
		Description: "Remove a host from the web_fetch URL allowlist (parent-only). " +
			"Example: allowlist_remove(host=\"example.com\")",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host": map[string]any{
					"type":        "string",
					"description": "The hostname to remove from the allowlist.",
				},
			},
			"required": []string{"host"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleAllowlistRemove dispatches builtin__allowlist_remove.
func HandleAllowlistRemove(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	host, _ := args["host"].(string)
	if host == "" {
		return "", fmt.Errorf("allowlist_remove requires a 'host' argument (e.g. allowlist_remove(host=\"example.com\"))")
	}

	// Canonicalize: accepts bare host, full URL, or URL with scheme/port.
	// Rejects anything that wouldn't match the web_fetch host gate.
	host, err := config.NormalizeAllowlistHost(host)
	if err != nil {
		return "", fmt.Errorf("allowlist_remove: %w", err)
	}

	// #423 round 18 (review F1 + S2): same exclusive-owner span as
	// HandleAllowlistAdd — the whole read-modify-write + persist is under the
	// config's shared policy owner so it never races hostDecision's snapshot.
	owner, err := deps.policyOwner()
	if err != nil {
		return "", err
	}
	owner.Lock()
	defer owner.Unlock()

	wf := &deps.Cfg.Tools.WebFetch
	found := false
	newAllowlist := make([]string, 0, len(wf.URLAllowlist))
	for _, existing := range wf.URLAllowlist {
		if strings.EqualFold(existing, host) {
			found = true
			continue // skip this entry
		}
		newAllowlist = append(newAllowlist, existing)
	}

	if !found {
		return fmt.Sprintf("%q is not in the allowlist", host), nil
	}

	wf.URLAllowlist = newAllowlist

	// Persist to disk immediately.
	if deps.ConfigPath != "" {
		pErr := writeConfigInline(deps.ConfigPath, deps.Cfg)
		if pErr != nil {
			log.Printf("[admin] failed to persist allowlist_remove: %v", pErr)
			return fmt.Sprintf("removed %q from in-memory allowlist, but failed to persist to disk: %v", host, pErr), nil
		}
	}

	auditArgs, _ := json.Marshal(map[string]any{"host": host})
	if err := logAudit(ctx, deps, toolNameAllowlistRemove, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameAllowlistRemove, err)
	}
	return fmt.Sprintf("removed %q from web_fetch URL allowlist (%d remaining)", host, len(newAllowlist)), nil
}

// writeConfigInline marshals the config and writes it to disk.
func writeConfigInline(cfgPath string, cfg *config.Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	header := "# FamClaw configuration (managed by web UI)\n# Edit via the Settings page in the web UI, or edit this file and restart.\n\n"
	if err := os.WriteFile(cfgPath, append([]byte(header), data...), 0600); err != nil {
		return fmt.Errorf("failed to write config file %s: %w", cfgPath, err)
	}
	return nil
}
