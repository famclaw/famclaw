// web_fetch_denylist.go implements the parent-only tools for maintaining the
// web_fetch URL denylist (tools.web_fetch.url_denylist), used when
// url_policy = "denylist" (the kid-safe default, design §1.1/§3). It mirrors
// web_fetch_allowlist.go exactly — same normalization, dedupe, persistence
// and audit patterns — so the two lists stay symmetric.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/config"
)

const (
	toolNameDenylistList   = "builtin__denylist_list"
	toolNameDenylistAdd    = "builtin__denylist_add"
	toolNameDenylistRemove = "builtin__denylist_remove"
)

// DenylistListDefinition returns the parent-only tool that lists all hosts in
// the web_fetch URL denylist plus the size of the bundled kid-safe baseline.
func DenylistListDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name: toolNameDenylistList,
		Description: "List all hosts in the web_fetch URL denylist (parent-only). " +
			"Shows every host the family has explicitly denied in denylist mode, plus the bundled baseline size.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []string{},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleDenylistList dispatches builtin__denylist_list.
func HandleDenylistList(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	hosts := deps.Cfg.Tools.WebFetch.URLDenylist
	if len(hosts) == 0 {
		return "web_fetch URL denylist is empty — no hosts are explicitly denied by the parent. (The bundled kid-safe baseline is always active in denylist mode.)", nil
	}
	var lines []string
	for _, h := range hosts {
		lines = append(lines, "  \u2022 "+h)
	}
	return fmt.Sprintf("web_fetch URL denylist (%d hosts):\n%s", len(hosts), strings.Join(lines, "\n")), nil
}

// DenylistAddDefinition returns the parent-only tool that adds a host to the
// web_fetch URL denylist.
func DenylistAddDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name: toolNameDenylistAdd,
		Description: "Add a host to the web_fetch URL denylist (parent-only). " +
			"Example: denylist_add(host=\"example.com\")",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host": map[string]any{
					"type":        "string",
					"description": "The hostname to deny (e.g. 'example.com' or 'sub.service.io').",
				},
			},
			"required": []string{"host"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleDenylistAdd dispatches builtin__denylist_add.
func HandleDenylistAdd(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	host, _ := args["host"].(string)
	if host == "" {
		return "", fmt.Errorf("denylist_add requires a 'host' argument (e.g. denylist_add(host=\"example.com\"))")
	}

	host, err := config.NormalizeAllowlistHost(host)
	if err != nil {
		return "", fmt.Errorf("denylist_add: %w", err)
	}

	for _, existing := range deps.Cfg.Tools.WebFetch.URLDenylist {
		if strings.EqualFold(existing, host) {
			return fmt.Sprintf("%q is already in the denylist", existing), nil
		}
	}

	deps.Cfg.Tools.WebFetch.URLDenylist = append(deps.Cfg.Tools.WebFetch.URLDenylist, host)

	if deps.ConfigPath != "" {
		if err := writeConfigInline(deps.ConfigPath, deps.Cfg); err != nil {
			log.Printf("[admin] failed to persist denylist_add: %v", err)
			return fmt.Sprintf("added %q to in-memory denylist, but failed to persist to disk: %v", host, err), nil
		}
	}

	auditArgs, _ := json.Marshal(map[string]any{"host": host})
	if err := logAudit(ctx, deps, toolNameDenylistAdd, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameDenylistAdd, err)
	}
	return fmt.Sprintf("added %q to web_fetch URL denylist (%d total)", host, len(deps.Cfg.Tools.WebFetch.URLDenylist)), nil
}

// DenylistRemoveDefinition returns the parent-only tool that removes a host
// from the web_fetch URL denylist.
func DenylistRemoveDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name: toolNameDenylistRemove,
		Description: "Remove a host from the web_fetch URL denylist (parent-only). " +
			"Example: denylist_remove(host=\"example.com\")",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host": map[string]any{
					"type":        "string",
					"description": "The hostname to remove from the denylist.",
				},
			},
			"required": []string{"host"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleDenylistRemove dispatches builtin__denylist_remove.
func HandleDenylistRemove(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	host, _ := args["host"].(string)
	if host == "" {
		return "", fmt.Errorf("denylist_remove requires a 'host' argument (e.g. denylist_remove(host=\"example.com\"))")
	}

	host, err := config.NormalizeAllowlistHost(host)
	if err != nil {
		return "", fmt.Errorf("denylist_remove: %w", err)
	}

	found := false
	newDenylist := make([]string, 0, len(deps.Cfg.Tools.WebFetch.URLDenylist))
	for _, existing := range deps.Cfg.Tools.WebFetch.URLDenylist {
		if strings.EqualFold(existing, host) {
			found = true
			continue
		}
		newDenylist = append(newDenylist, existing)
	}

	if !found {
		return fmt.Sprintf("%q is not in the denylist", host), nil
	}

	deps.Cfg.Tools.WebFetch.URLDenylist = newDenylist

	if deps.ConfigPath != "" {
		if err := writeConfigInline(deps.ConfigPath, deps.Cfg); err != nil {
			log.Printf("[admin] failed to persist denylist_remove: %v", err)
			return fmt.Sprintf("removed %q from in-memory denylist, but failed to persist to disk: %v", host, err), nil
		}
	}

	auditArgs, _ := json.Marshal(map[string]any{"host": host})
	if err := logAudit(ctx, deps, toolNameDenylistRemove, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameDenylistRemove, err)
	}
	return fmt.Sprintf("removed %q from web_fetch URL denylist (%d remaining)", host, len(newDenylist)), nil
}
