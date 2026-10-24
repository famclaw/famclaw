package mcpserver

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// New builds the android-control MCP server with all v1 tools registered.
// The policy controls which tools are exposed (shell is gated behind
// policy.Tools.Shell, default off). Production wiring: the executor shells
// out to the host adb binary.
func New(policy *PolicyConfig, version string) *server.MCPServer {
	adb := NewRealADBExecutor(policy.DeviceAlias, policy.DeviceSerial)
	return NewWithExecutor(policy, version, adb)
}

// NewWithExecutor builds the server with an injected executor so tests can
// back it with a fake adb runner (design §6.1 fake-adb seam).
func NewWithExecutor(policy *PolicyConfig, version string, adb *ADBExecutor) *server.MCPServer {
	srv := server.NewMCPServer("android-control", version,
		server.WithInstructions(
			"Control an Android device via ADB. "+
				"Every mutating tool refuses unless the screen is on and unlocked. "+
				"The butler never enters credentials, one-time codes, or passcodes. "+
				"If the screen shows a login, sign-up, 2FA, or paywall, stop and report it.",
		),
	)

	addTools(srv, policy, adb)
	return srv
}

// addTools registers all v1 tool handlers on the server.
// Every tool routes through dispatch — the single entry point where the
// policy engine guards each call (allowlists, screen-state gating, rate
// limit, credential guard, no-leak) before adb execution.
func addTools(srv *server.MCPServer, policy *PolicyConfig, adb *ADBExecutor) {
	pe := NewPolicyEngine(policy, adb)
	h := func(name string) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return dispatch(ctx, pe, name, req.GetArguments())
		}
	}

	srv.AddTool(mcp.NewTool("device.status",
		mcp.WithDescription("Report device state: booted, screen_on, lock_screen, and current foreground package."),
	), h("device.status"))

	srv.AddTool(mcp.NewTool("screenshot",
		mcp.WithDescription("Capture the current screen and return it as a PNG image."),
	), h("screenshot"))

	srv.AddTool(mcp.NewTool("find",
		mcp.WithDescription("Search the UI hierarchy for nodes matching text, content-desc, or resource-id. Returns matching nodes with bounds (center x,y) and class."),
		mcp.WithString("text", mcp.Description("Visible text to match")),
		mcp.WithString("content_desc", mcp.Description("Content description to match")),
		mcp.WithString("resource_id", mcp.Description("Android resource-id to match")),
	), h("find"))

	srv.AddTool(mcp.NewTool("tap",
		mcp.WithDescription("Tap a screen element. Provide a selector (text / content_desc / resource_id) to tap the first matching element's center, resolved from the live UI hierarchy; or explicit x/y pixel coordinates when no selector is given. Selector takes precedence."),
		mcp.WithString("text", mcp.Description("Visible text of the element to tap (selector)")),
		mcp.WithString("content_desc", mcp.Description("Content description of the element to tap (selector)")),
		mcp.WithString("resource_id", mcp.Description("Android resource-id of the element to tap (selector)")),
		mcp.WithNumber("x", mcp.Description("X pixel coordinate (used when no selector is given)")),
		mcp.WithNumber("y", mcp.Description("Y pixel coordinate (used when no selector is given)")),
	), h("tap"))

	srv.AddTool(mcp.NewTool("type",
		mcp.WithDescription("Type text into the focused input field. Spaces and special characters are handled per-character."),
		mcp.WithString("text", mcp.Required(), mcp.Description("Text to type")),
	), h("type"))

	srv.AddTool(mcp.NewTool("keyevent",
		mcp.WithDescription("Send an Android key event: home, back, recents, or menu."),
		mcp.WithString("key", mcp.Required(), mcp.Description("Key name: home, back, recents, menu")),
	), h("keyevent"))

	srv.AddTool(mcp.NewTool("swipe",
		mcp.WithDescription("Swipe from (x1,y1) to (x2,y2) over the given duration."),
		mcp.WithNumber("x1", mcp.Required(), mcp.Description("Start X")),
		mcp.WithNumber("y1", mcp.Required(), mcp.Description("Start Y")),
		mcp.WithNumber("x2", mcp.Required(), mcp.Description("End X")),
		mcp.WithNumber("y2", mcp.Required(), mcp.Description("End Y")),
		mcp.WithNumber("ms", mcp.Description("Duration in milliseconds, default 300")),
	), h("swipe"))

	srv.AddTool(mcp.NewTool("open_app",
		mcp.WithDescription("Open an application by Android package name. The package must be in the allowlist."),
		mcp.WithString("package", mcp.Required(), mcp.Description("Android package name, e.g. com.example.app")),
	), h("open_app"))

	// shell — gated behind policy.Tools["shell"] (default: off).
	if policy.Tools != nil && policy.Tools["shell"] {
		srv.AddTool(mcp.NewTool("shell",
			mcp.WithDescription("Execute an arbitrary adb shell command. Operator-only; enabled by policy config."),
			mcp.WithString("cmd", mcp.Required(), mcp.Description("Shell command to execute on device")),
		), h("shell"))
	}
}

// dispatch routes a tool call to its handler. Read tools (device.status,
// screenshot, find) and action tools (tap, type, keyevent, swipe, open_app,
// shell) all run through the adb executor. Every call is first guarded by
// the PolicyEngine (allowlists, screen-state gating, foreground-package
// check, credential hard-stop, rate limit, shell destructive refuse); the
// result passes through the no-leak sanitizer before it returns.
func dispatch(ctx context.Context, pe *PolicyEngine, tool string, args map[string]any) (*mcp.CallToolResult, error) {
	// Stage 4: the policy gate runs before any tool reaches ADB.
	if err := pe.Guard(ctx, tool, args); err != nil {
		return mcp.NewToolResultText(
			fmt.Sprintf("[android-control] policy: %s", pe.sanitizeError(err).Error()),
		), nil
	}

	adb := pe.ADB()
	var res *mcp.CallToolResult
	var err error
	switch tool {
	case "device.status":
		res, err = handleStatus(ctx, adb)
	case "screenshot":
		res, err = handleScreenshot(ctx, adb)
	case "find":
		res, err = handleFind(ctx, adb, args)
	case "tap":
		res, err = handleTap(ctx, adb, args)
	case "type":
		res, err = handleType(ctx, adb, args)
	case "keyevent":
		res, err = handleKeyEvent(ctx, adb, args)
	case "swipe":
		res, err = handleSwipe(ctx, adb, args)
	case "open_app":
		res, err = handleOpenApp(ctx, adb, args)
	case "shell":
		res, err = handleShell(ctx, adb, args)
	default:
		return mcp.NewToolResultText(
			fmt.Sprintf("[android-control] %s: unknown tool.", tool),
		), nil
	}
	if err != nil {
		// Defense in depth: handler errors carry executor text that may
		// include the serial; redact before it reaches the MCP error path.
		return res, pe.sanitizeError(err)
	}
	return pe.SanitizeResult(res), nil
}
