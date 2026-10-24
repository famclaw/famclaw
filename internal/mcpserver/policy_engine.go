package mcpserver

import (
	"context"
	"fmt"
	"log"
)

// PolicyEngine guards every tool call before it reaches ADB (stage 4, design
// §5). It wraps the PolicyConfig and ADBExecutor and enforces:
// per-tool kill switches, screen-on gate, foreground-package allowlist,
// credential-content hard stop, shell destructive-pattern refuse, per-tool
// rate limits, and no-leak result sanitisation.
type PolicyEngine struct {
	cfg *PolicyConfig
	adb *ADBExecutor
	rl  *rateLimiter
}

// NewPolicyEngine builds the engine from the loaded policy and the executor
// that will actually run adb commands.
func NewPolicyEngine(cfg *PolicyConfig, adb *ADBExecutor) *PolicyEngine {
	return &PolicyEngine{cfg: cfg, adb: adb, rl: newRateLimiter()}
}

// ADB returns the underlying executor so dispatch can pass it to handlers.
func (pe *PolicyEngine) ADB() *ADBExecutor { return pe.adb }

// Guard runs the full policy gate for one tool call. It returns nil when the
// call is allowed; a wrapped error when it must be refused. The error text is
// safe to show to the LLM (no-leak guaranteed by Sanitize).
func (pe *PolicyEngine) Guard(ctx context.Context, tool string, args map[string]any) error {
	// 1. Per-tool kill switch (tools map; default-on when absent).
	if !pe.cfg.ToolEnabled(tool) {
		pe.audit(tool, false, "tool disabled by policy")
		return fmt.Errorf("tool %q is disabled by policy", tool)
	}

	isMutation := tool == "tap" || tool == "type" || tool == "keyevent" ||
		tool == "swipe" || tool == "open_app" || tool == "shell"

	// 2. Screen-on gate for mutating actions.
	if isMutation {
		if err := pe.checkScreenOn(ctx); err != nil {
			pe.audit(tool, false, err.Error())
			return err
		}
	}

	// 3. Foreground-package allowlist for tap / type.
	if tool == "tap" || tool == "type" {
		if err := pe.checkForeground(ctx); err != nil {
			pe.audit(tool, false, err.Error())
			return err
		}
	}

	// 4. Credential-content hard stop for type.
	if tool == "type" {
		if text, _ := args["text"].(string); text != "" {
			if err := checkCredentialContent(text); err != nil {
				pe.audit(tool, false, err.Error())
				return err
			}
		}
	}

	// 5. open_app: target package must be in full mode.
	if tool == "open_app" {
		if pkg, _ := args["package"].(string); pkg != "" {
			if mode := pe.cfg.AppMode(pkg); mode != "full" {
				pe.audit(tool, false, fmt.Sprintf("target app %q is %s, need full", pkg, mode))
				return fmt.Errorf("app %q is in %q mode; open_app requires full mode", pkg, mode)
			}
		}
	}

	// 6. Shell destructive-pattern refuse.
	if tool == "shell" {
		if cmd, _ := args["cmd"].(string); cmd != "" {
			if err := checkShellDestructive(cmd); err != nil {
				pe.audit(tool, false, err.Error())
				return err
			}
		}
	}

	// 7. Rate limit.
	if err := pe.rl.allow(tool); err != nil {
		pe.audit(tool, false, err.Error())
		return err
	}

	pe.audit(tool, true, "")
	return nil
}

// checkScreenOn refuses mutating actions when the screen is off.
func (pe *PolicyEngine) checkScreenOn(ctx context.Context) error {
	on, err := pe.adb.ScreenOn(ctx)
	if err != nil {
		return fmt.Errorf("checking screen state: %w", err)
	}
	if !on {
		return fmt.Errorf("screen is off; refusing mutating action")
	}
	return nil
}

// checkForeground verifies the foreground app is in full mode.
func (pe *PolicyEngine) checkForeground(ctx context.Context) error {
	pkg, err := pe.adb.ForegroundPackage(ctx)
	if err != nil {
		return fmt.Errorf("reading foreground app: %w", err)
	}
	if pkg == "" {
		return fmt.Errorf("no foreground app detected; refusing")
	}
	mode := pe.cfg.AppMode(pkg)
	switch mode {
	case "full":
		return nil
	case "deny":
		return fmt.Errorf("app %q is on the deny list; refusing", pkg)
	default:
		return fmt.Errorf("app %q is in read-only mode; refusing mutating action", pkg)
	}
}

// audit writes a structured log line to stderr (stdout is reserved for MCP
// JSON-RPC). The device alias is used, never the serial.
func (pe *PolicyEngine) audit(tool string, allowed bool, reason string) {
	alias := pe.cfg.DeviceAlias
	if alias == "" {
		alias = "unknown-device"
	}
	if allowed {
		log.Printf("[android-control] audit device=%s tool=%s allowed=true", alias, tool)
	} else {
		log.Printf("[android-control] audit device=%s tool=%s allowed=false reason=%q", alias, tool, reason)
	}
}
