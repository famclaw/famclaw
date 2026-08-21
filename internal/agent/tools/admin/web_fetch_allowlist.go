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
	hosts := deps.Cfg.Tools.WebFetch.URLAllowlist
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
	host = strings.TrimSpace(host)

	// Strip scheme, path, port — we only store bare hostnames
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	host = strings.Split(host, "/")[0]
	host = strings.Split(host, ":")[0] // strip port for now
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("allowlist_add: host must be a valid hostname")
	}

	// Check for duplicate (case-insensitive)
	for _, existing := range deps.Cfg.Tools.WebFetch.URLAllowlist {
		if strings.EqualFold(existing, host) {
			return fmt.Sprintf("%q is already in the allowlist", existing), nil
		}
	}

	// Append to allowlist in-memory
	deps.Cfg.Tools.WebFetch.URLAllowlist = append(deps.Cfg.Tools.WebFetch.URLAllowlist, host)

	// Persist to disk immediately
	if deps.ConfigPath != "" {
		if err := writeConfigInline(deps.ConfigPath, deps.Cfg); err != nil {
			log.Printf("[admin] failed to persist allowlist_add: %v", err)
			return fmt.Sprintf("added %q to in-memory allowlist, but failed to persist to disk: %v", host, err), nil
		}
	}

	auditArgs, _ := json.Marshal(map[string]any{"host": host})
	if err := logAudit(ctx, deps, toolNameAllowlistAdd, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameAllowlistAdd, err)
	}
	return fmt.Sprintf("added %q to web_fetch URL allowlist (%d total)", host, len(deps.Cfg.Tools.WebFetch.URLAllowlist)), nil
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
	host = strings.TrimSpace(host)

	// Strip scheme, path, port
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	host = strings.Split(host, "/")[0]
	host = strings.Split(host, ":")[0]
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("allowlist_remove: host must be a valid hostname")
	}

	found := false
	newAllowlist := make([]string, 0, len(deps.Cfg.Tools.WebFetch.URLAllowlist))
	for _, existing := range deps.Cfg.Tools.WebFetch.URLAllowlist {
		if strings.EqualFold(existing, host) {
			found = true
			continue // skip this entry
		}
		newAllowlist = append(newAllowlist, existing)
	}

	if !found {
		return fmt.Sprintf("%q is not in the allowlist", host), nil
	}

	deps.Cfg.Tools.WebFetch.URLAllowlist = newAllowlist

	// Persist to disk immediately
	if deps.ConfigPath != "" {
		if err := writeConfigInline(deps.ConfigPath, deps.Cfg); err != nil {
			log.Printf("[admin] failed to persist allowlist_remove: %v", err)
			return fmt.Sprintf("removed %q from in-memory allowlist, but failed to persist to disk: %v", host, err), nil
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
