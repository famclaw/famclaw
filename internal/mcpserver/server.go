package mcpserver

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// New builds the android-control MCP server with all v1 tools registered.
// The policy controls which tools are exposed (shell is gated behind
// policy.Tools.Shell, default off). In this skeleton card the dispatch body
// returns a "not implemented" stub; the tool registry and JSON schemas are
// final so the server already lists correctly over stdio.
func New(policy *PolicyConfig, version string) *server.MCPServer {
	srv := server.NewMCPServer("android-control", version,
		server.WithInstructions(
			"Control an Android device via ADB. "+
				"Every mutating tool refuses unless the screen is on and unlocked. "+
				"The butler never enters credentials, one-time codes, or passcodes. "+
				"If the screen shows a login, sign-up, 2FA, or paywall, stop and report it.",
		),
	)

	addTools(srv, policy)
	return srv
}

// addTools registers all v1 tool handlers on the server.
// Every tool routes through dispatch — the single entry point where the
// policy check (later card) and adb execution will be inserted.
func addTools(srv *server.MCPServer, policy *PolicyConfig) {
	h := func(name string) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return dispatch(ctx, name, req.GetArguments())
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
		mcp.WithDescription("Tap at screen coordinates. Coordinates should come from a prior find call."),
		mcp.WithNumber("x", mcp.Required(), mcp.Description("X coordinate in pixels")),
		mcp.WithNumber("y", mcp.Required(), mcp.Description("Y coordinate in pixels")),
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

	// shell — gated behind policy.Tools.Shell (default: off).
	if policy.Tools.Shell {
		srv.AddTool(mcp.NewTool("shell",
			mcp.WithDescription("Execute an arbitrary adb shell command. Operator-only; enabled by policy config."),
			mcp.WithString("cmd", mcp.Required(), mcp.Description("Shell command to execute on device")),
		), h("shell"))
	}
}

// dispatch is the single entry point every tool handler calls. In this
// skeleton card it returns a "not implemented" stub; the next card inserts
// the policy check (allowlist, screen-state, rate-limit, credential guard)
// and the adb execution here.
func dispatch(_ context.Context, tool string, _ map[string]any) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText(
		fmt.Sprintf("[android-control] %s: not implemented yet (skeleton). This tool is registered but the adb backend is not wired.", tool),
	), nil
}
