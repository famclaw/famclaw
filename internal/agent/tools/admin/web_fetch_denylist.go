// web_fetch_denylist.go implements the parent-only tools for maintaining the
// web_fetch URL denylist (tools.web_fetch.url_denylist), used when
// url_policy = "denylist" (the kid-safe default, design §1.1/§3). It mirrors
// web_fetch_allowlist.go exactly — same normalization, dedupe, persistence
// and audit patterns — so the two lists stay symmetric.
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
	// Read-lock so the slice-header read cannot tear against a concurrent
	// denylist_add/remove rewrite sharing the same owner; policyOwner()
	// fails closed when the owner is unwired (ErrPolicyOwnerUnwired).
	owner, err := deps.policyOwner()
	if err != nil {
		return "", err
	}
	owner.RLock()
	defer owner.RUnlock()
	return formatDenylistList(deps.Cfg.Tools.WebFetch.URLDenylist), nil
}

func formatDenylistList(hosts []string) string {
	if len(hosts) == 0 {
		return "web_fetch URL denylist is empty — no hosts are explicitly denied by the parent. (The bundled kid-safe baseline is always active in denylist mode.)"
	}
	var lines []string
	for _, h := range hosts {
		lines = append(lines, "  \u2022 "+h)
	}
	return fmt.Sprintf("web_fetch URL denylist (%d hosts):\n%s", len(hosts), strings.Join(lines, "\n"))
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

	// #423 round 18 lock order (plan §C, scoped): the config's ONE shared
	// policy owner (M) is taken with no other lock held and spans the
	// whole read-modify-write + commit; SaveConfig takes T
	// (SharedConfigManager.mu) inside M, never the reverse. hostDecision
	// on every Agent snapshots under M's read lock, so this writer
	// excludes all decision-time readers process-wide. policyOwner() fails
	// closed when the owner is unwired (ErrPolicyOwnerUnwired).
	owner, err := deps.policyOwner()
	if err != nil {
		return "", err
	}
	owner.Lock()

	wf := &deps.Cfg.Tools.WebFetch
	for _, existing := range wf.URLDenylist {
		if strings.EqualFold(existing, host) {
			owner.Unlock()
			return fmt.Sprintf("%q is already in the denylist", existing), nil
		}
	}

	// S4 (fail-closed precondition): before building or saving, read the
	// on-disk content token. A read failure (file present but unreadable)
	// means the disk state is unknown — abort with no mutation, instead of
	// saving against a blind overwrite. (A missing file reads as "" —
	// precondition vacuous by design for new files.)
	persist := deps.ConfigPath != "" && deps.SharedConfig != nil
	var token string
	if persist {
		if token, err = deps.SharedConfig.CurrentToken(); err != nil {
			owner.Unlock()
			return "", fmt.Errorf("denylist_add: reading config content token: %w", err)
		}
	}

	// Plan §B commit order (scoped, live config): publish the in-memory
	// mutation under M, commit the file under T (inside M — the observed
	// order is always M -> T, never T -> M), and roll back on operator
	// conflict so existing config is preserved (review S3). `next` is a
	// fresh backing array: publishing it can never mutate the old array a
	// decision-time snapshot is iterating.
	next := make([]string, len(wf.URLDenylist), len(wf.URLDenylist)+1)
	copy(next, wf.URLDenylist)
	next = append(next, host)

	previous := wf.URLDenylist
	wf.URLDenylist = next

	if persist {
		if _, err = deps.SharedConfig.SaveConfig(deps.Cfg, token); err != nil {
			if errors.Is(err, ErrOperatorConflict) {
				// S3: the disk changed after the precondition read —
				// roll back the in-memory mutation and surface the
				// sentinel for caller re-read + retry.
				wf.URLDenylist = previous
				owner.Unlock()
				return "", err
			}
			// B3: non-conflict commit failure — keep the in-memory
			// change and report the persistence failure.
			owner.Unlock()
			log.Printf("[admin] failed to persist denylist_add: %v", err)
			return fmt.Sprintf("added %q to in-memory denylist, but failed to persist to disk: %v", host, err), nil
		}
	}
	// Publish confirmed (or memory-only entry point).
	owner.Unlock()

	// Plan §C: DB audit runs WITHOUT M (no lock held) — and the response
	// is built from the local `next`, never a re-read of the (now
	// unlocked) shared slice.
	auditArgs, _ := json.Marshal(map[string]any{"host": host})
	if err := logAudit(ctx, deps, toolNameDenylistAdd, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameDenylistAdd, err)
	}
	return fmt.Sprintf("added %q to web_fetch URL denylist (%d total)", host, len(next)), nil
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

	// Same shared-owner span as HandleDenylistAdd: M taken first, no other
	// lock held; SaveConfig takes T inside M; audit runs without M.
	// policyOwner() fails closed when the owner is unwired
	// (ErrPolicyOwnerUnwired).
	owner, err := deps.policyOwner()
	if err != nil {
		return "", err
	}
	owner.Lock()

	wf := &deps.Cfg.Tools.WebFetch
	found := false
	newDenylist := make([]string, 0, len(wf.URLDenylist))
	for _, existing := range wf.URLDenylist {
		if strings.EqualFold(existing, host) {
			found = true
			continue
		}
		newDenylist = append(newDenylist, existing)
	}

	if !found {
		owner.Unlock()
		return fmt.Sprintf("%q is not in the denylist", host), nil
	}

	// S4 fail-closed precondition (same as add).
	persist := deps.ConfigPath != "" && deps.SharedConfig != nil
	var token string
	if persist {
		if token, err = deps.SharedConfig.CurrentToken(); err != nil {
			owner.Unlock()
			return "", fmt.Errorf("denylist_remove: reading config content token: %w", err)
		}
	}

	// Same plan §B commit order as add: publish under M, commit under T
	// inside M, roll back on operator conflict (S3).
	previous := wf.URLDenylist
	wf.URLDenylist = newDenylist

	if persist {
		if _, err = deps.SharedConfig.SaveConfig(deps.Cfg, token); err != nil {
			if errors.Is(err, ErrOperatorConflict) {
				// S3: roll back the in-memory mutation; surface the
				// sentinel for caller re-read + retry.
				wf.URLDenylist = previous
				owner.Unlock()
				return "", err
			}
			// B3: keep the in-memory change, report the persistence failure.
			owner.Unlock()
			log.Printf("[admin] failed to persist denylist_remove: %v", err)
			return fmt.Sprintf("removed %q from in-memory denylist, but failed to persist to disk: %v", host, err), nil
		}
	}
	// Publish confirmed (or memory-only entry point).
	owner.Unlock()

	// Plan §C: DB audit runs WITHOUT M (no lock held).
	auditArgs, _ := json.Marshal(map[string]any{"host": host})
	if err := logAudit(ctx, deps, toolNameDenylistRemove, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameDenylistRemove, err)
	}
	return fmt.Sprintf("removed %q from web_fetch URL denylist (%d remaining)", host, len(newDenylist)), nil
}
