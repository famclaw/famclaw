// Package filesend implements the builtin__send_file tool, which delivers a
// file from the current conversation's sandbox back to the requester's own
// chat. It closes the outbound half of the file story: inbound attachments
// land in the sandbox (gateway adapters) and outbound files were previously
// impossible — gateway.Reply carried text only, so a file the agent produced
// (e.g. via file_write) could never reach the user.
//
// Safety model: what may be read is bounded by the per-conversation sandbox
// (the same confinePath guarantee as file_read), and where it goes is the
// requester's own channel or DM (no cross-user targeting). Which roles may
// call the tool is decided by OPA tool_policy.
package filesend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/gateway"
)

// ToolName is the name of the send_file tool.
const ToolName = "builtin__send_file"

// MaxFileBytes caps outbound files at Discord's default upload limit for
// bots (25 MiB) — the only gateway with a FileSender today.
const MaxFileBytes = 25 * 1024 * 1024

// SendTimeout bounds a single delivery so a slow gateway cannot stall the
// agent tool loop. Mirrors sendmsg.SendTimeout.
const SendTimeout = 30 * time.Second

// Tool returns the agentcore.Tool definition for builtin__send_file.
// The tool is registered for all roles; OPA tool_policy decides who may
// actually call it (see tool_policy.rego).
func Tool() agentcore.Tool {
	return agentcore.Tool{
		Name:        ToolName,
		Description: "Send a file from this conversation's workspace to the user (an image, document, or any file you created). 'path' uses the same rules as file_read. Call it after the file exists — for example after file_write creates a report or a tool produces an image. 'caption' is an optional short message shown alongside the file.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File to send, relative to the conversation workspace (same rules as file_read).",
				},
				"caption": map[string]any{
					"type":        "string",
					"description": "Optional short message to show alongside the file.",
				},
			},
			"required": []string{"path"},
		},
		Source: "builtin",
	}
}

// DB is the store surface filesend needs. *store.DB implements it.
type DB interface {
	LogAudit(ctx context.Context, actorName, gateway, toolName string, args []byte) error
}

// ErrNoFileSenderForGateway is returned when the current gateway has no
// FileSender implementation (file delivery is not available there yet).
var ErrNoFileSenderForGateway = errors.New("file delivery is not available on this gateway yet")

// Handle confines path to the conversation sandbox, enforces the size and
// filename limits, and delivers the file through the FileSender registered
// for the current gateway. On success it records an audit entry and returns
// a confirmation string for the LLM transcript.
//
// auditGateway is the gateway recorded in the audit log (may differ from
// deliveryGateway for web-originated conversations). sandboxRoot is the
// conversation sandbox root ("" disables the sandbox — the call fails).
func Handle(ctx context.Context, db DB, fileSenders map[string]gateway.FileSender, actor, auditGateway, deliveryGateway string, dest gateway.OutboundDestination, sandboxRoot, path, caption string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("send_file requires a 'path' argument")
	}
	absPath, err := ConfinePath(sandboxRoot, path)
	if err != nil {
		return "", fmt.Errorf("resolving send_file path: %w", err)
	}
	fi, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("stating file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("send_file: %q is not a regular file", path)
	}
	if fi.Size() > MaxFileBytes {
		return "", fmt.Errorf("send_file: %q is %d bytes, above the %d byte delivery limit", filepath.Base(absPath), fi.Size(), MaxFileBytes)
	}
	name := filepath.Base(absPath)

	sender, ok := fileSenders[deliveryGateway]
	if !ok || sender == nil {
		return "", fmt.Errorf("%w: %q", ErrNoFileSenderForGateway, deliveryGateway)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}

	deadlineCtx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	if err := sender.SendFile(deadlineCtx, dest, gateway.OutboundFile{Name: name, Data: data}, caption); err != nil {
		return "", fmt.Errorf("sending file via %s: %w", deliveryGateway, err)
	}

	// Audit: who sent which file where. Never leak the content — files may
	// hold sensitive data, and the transcript already records the tool call.
	if db != nil {
		auditArgs := map[string]any{
			"path":    path,
			"name":    name,
			"bytes":   fi.Size(),
			"caption": caption,
		}
		if b, jerr := json.Marshal(auditArgs); jerr == nil {
			_ = db.LogAudit(ctx, actor, auditGateway, ToolName, b)
		}
	}
	return fmt.Sprintf("sent %q (%d bytes) to the user", name, fi.Size()), nil
}

// ConfinePath resolves path against the conversation sandbox root and
// returns the absolute, symlink-resolved target, refusing any path that
// escapes the root. It is the shared containment rule for every tool that
// touches conversation files (file_read/write/stat/list and send_file), so
// the escape checks live in one place.
//
// A relative path is joined under the root; an absolute path is accepted
// only if it stays inside the root. EvalSymlinks defeats symlink escapes
// (a link inside the sandbox pointing out resolves outside and is rejected
// by the filepath.Rel check).
func ConfinePath(sandboxRoot, path string) (string, error) {
	if sandboxRoot == "" {
		return "", errors.New("sandbox root not configured")
	}
	// Ensure sandbox root is absolute and evaluated for symlinks.
	root, err := filepath.EvalSymlinks(filepath.Clean(sandboxRoot))
	if err != nil {
		return "", fmt.Errorf("invalid sandbox root: %w", err)
	}
	var absPath string
	if filepath.IsAbs(path) {
		if absPath, err = filepath.EvalSymlinks(filepath.Clean(path)); err != nil {
			return "", fmt.Errorf("failed to clean path: %w", err)
		}
	} else {
		if absPath, err = filepath.EvalSymlinks(filepath.Clean(filepath.Join(root, path))); err != nil {
			return "", fmt.Errorf("failed to join and clean path: %w", err)
		}
	}
	// Check that the path is within the sandbox root using filepath.Rel to
	// avoid string prefix issues.
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return "", fmt.Errorf("computing relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q escapes sandbox root %q", path, root)
	}
	return absPath, nil
}
