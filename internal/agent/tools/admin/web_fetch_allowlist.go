package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/config"
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
		return "", fmt.Errorf("allowlist_add requires a 'host' argument (e.g. allowlist_add(host=%q))", "example.com")
	}

	// Canonicalize: accepts bare host, full URL, or URL with scheme/port.
	// Rejects anything that wouldn't match the web_fetch host gate.
	host, err := config.NormalizeAllowlistHost(host)
	if err != nil {
		return "", fmt.Errorf("allowlist_add: %w", err)
	}

	// #423 round 18 (review F1 + S2): the config's ONE shared policy owner
	// (M) is taken with no other lock held and spans the whole
	// read-modify-write + commit; SaveConfig takes T inside M. Before this
	// round the allowlist handlers took NO lock at all and rewrote
	// Cfg.Tools.WebFetch.URLAllowlist unsynchronized against hostDecision's
	// decision-time snapshot.
	owner, err := deps.policyOwner()
	if err != nil {
		return "", err
	}
	owner.Lock()

	wf := &deps.Cfg.Tools.WebFetch
	for _, existing := range wf.URLAllowlist {
		if strings.EqualFold(existing, host) {
			owner.Unlock()
			return fmt.Sprintf("%q is already in the allowlist", existing), nil
		}
	}

	// S4 (fail-closed precondition): read the on-disk content token before
	// building or saving; a read failure aborts with no mutation.
	persist := deps.ConfigPath != "" && deps.SharedConfig != nil
	var token string
	if persist {
		if token, err = deps.SharedConfig.CurrentToken(); err != nil {
			owner.Unlock()
			return "", fmt.Errorf("allowlist_add: reading config content token: %w", err)
		}
	}

	// Plan §B commit order (scoped, live config): publish the in-memory
	// mutation under M, commit the file under T (inside M — the observed
	// order is always M -> T, never T -> M), and roll back on operator
	// conflict so existing config is preserved (review S3). `next` is a
	// fresh backing array: publishing it can never mutate the old array a
	// decision-time snapshot is iterating.
	next := make([]string, len(wf.URLAllowlist), len(wf.URLAllowlist)+1)
	copy(next, wf.URLAllowlist)
	next = append(next, host)

	previous := wf.URLAllowlist
	wf.URLAllowlist = next

	if persist {
		if _, err = deps.SharedConfig.SaveConfig(deps.Cfg, token); err != nil {
			if errors.Is(err, ErrOperatorConflict) {
				// S3: the disk changed after the precondition read —
				// roll back the in-memory mutation and surface the
				// sentinel for caller re-read + retry.
				wf.URLAllowlist = previous
				owner.Unlock()
				return "", err
			}
			// B3: non-conflict commit failure — keep the in-memory
			// change and report the persistence failure.
			owner.Unlock()
			log.Printf("[admin] failed to persist allowlist_add: %v", err)
			return fmt.Sprintf("added %q to in-memory allowlist, but failed to persist to disk: %v", host, err), nil
		}
	}
	// Publish confirmed (or memory-only entry point).
	owner.Unlock()

	// Plan §C: DB audit runs WITHOUT M (no lock held) — and the response
	// is built from the local `next`, never a re-read of the (now
	// unlocked) shared slice.
	auditArgs, _ := json.Marshal(map[string]any{"host": host})
	if err := logAudit(ctx, deps, toolNameAllowlistAdd, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameAllowlistAdd, err)
	}
	return fmt.Sprintf("added %q to web_fetch URL allowlist (%d total)", host, len(next)), nil
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
		return "", fmt.Errorf("allowlist_remove requires a 'host' argument (e.g. allowlist_remove(host=%q))", "example.com")
	}

	// Canonicalize: accepts bare host, full URL, or URL with scheme/port.
	// Rejects anything that wouldn't match the web_fetch host gate.
	host, err := config.NormalizeAllowlistHost(host)
	if err != nil {
		return "", fmt.Errorf("allowlist_remove: %w", err)
	}

	// Same shared-owner span as HandleAllowlistAdd (review F1 + S2).
	owner, err := deps.policyOwner()
	if err != nil {
		return "", err
	}
	owner.Lock()

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
		owner.Unlock()
		return fmt.Sprintf("%q is not in the allowlist", host), nil
	}

	// S4 fail-closed precondition (same as add).
	persist := deps.ConfigPath != "" && deps.SharedConfig != nil
	var token string
	if persist {
		if token, err = deps.SharedConfig.CurrentToken(); err != nil {
			owner.Unlock()
			return "", fmt.Errorf("allowlist_remove: reading config content token: %w", err)
		}
	}

	// Same plan §B commit order as add: publish under M, commit under T
	// inside M, roll back on operator conflict (S3).
	previous := wf.URLAllowlist
	wf.URLAllowlist = newAllowlist

	if persist {
		if _, err = deps.SharedConfig.SaveConfig(deps.Cfg, token); err != nil {
			if errors.Is(err, ErrOperatorConflict) {
				// S3: roll back the in-memory mutation; surface the
				// sentinel for caller re-read + retry.
				wf.URLAllowlist = previous
				owner.Unlock()
				return "", err
			}
			// B3: keep the in-memory change, report the persistence failure.
			owner.Unlock()
			log.Printf("[admin] failed to persist allowlist_remove: %v", err)
			return fmt.Sprintf("removed %q from in-memory allowlist, but failed to persist to disk: %v", host, err), nil
		}
	}
	// Publish confirmed (or memory-only entry point).
	owner.Unlock()

	// Plan §C: DB audit runs WITHOUT M.
	auditArgs, _ := json.Marshal(map[string]any{"host": host})
	if err := logAudit(ctx, deps, toolNameAllowlistRemove, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameAllowlistRemove, err)
	}
	return fmt.Sprintf("removed %q from web_fetch URL allowlist (%d remaining)", host, len(newAllowlist)), nil
}
