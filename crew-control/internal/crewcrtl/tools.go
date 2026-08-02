package crewcrtl

import (
	"context"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Tool names. These MUST match the tool names referenced in SKILL.md,
// because famclaw injects the SKILL.md body into the system prompt (telling
// the model which tools to call) and the MCP pool exposes these exact names
// to the agent's tool loop.
const (
	ToolFleetOverview = "fleet_overview"
	ToolCrewState     = "crew_state"
	ToolBacklog       = "backlog"
)

// readOnlyOpts returns tool options that mark a tool as read-only,
// non-destructive, and idempotent. Every tool in this addon is read-only;
// these hints tell MCP clients (and famclaw's tool registry) that the tools
// never mutate state.
func readOnlyOpts() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithToolAnnotation(mcp.ToolAnnotation{
			ReadOnlyHint:    mcp.ToBoolPtr(true),
			DestructiveHint: mcp.ToBoolPtr(false),
			IdempotentHint:  mcp.ToBoolPtr(true),
			OpenWorldHint:   mcp.ToBoolPtr(true),
		}),
	}
}

// --- Tool argument structs ------------------------------------------------

type crewStateArgs struct {
	CrewID string `json:"crew_id"`
}

// --- Tool definitions -----------------------------------------------------

// fleetOverviewTool returns the whole-fleet Markdown view from
// fm-fleet-view.sh. No arguments required.
var fleetOverviewTool = mcp.NewTool(ToolFleetOverview,
	append(readOnlyOpts(),
		mcp.WithDescription(
			"Get a whole-fleet overview from the firstmate fleet view. Shows all crews "+
				"(in-flight, queued, done) with their current state, backend, endpoint, "+
				"artifact, and watch channel. This is a READ-ONLY tool — it does not start, "+
				"stop, or modify any crew. Use this when the captain asks what the crews are doing.",
		),
	)...)

// crewStateTool returns the current state of a single crew by its ID.
var crewStateTool = mcp.NewTool(ToolCrewState,
	append(readOnlyOpts(),
		mcp.WithDescription(
			"Get the current state of a single crew by its firstmate crew ID. "+
				"Returns one line: \"state: <state> · source: <source> · <detail>\". "+
				"This is a READ-ONLY tool — it does not start, stop, or modify the crew. "+
				"The crew_id must be a simple identifier (letters, digits, dashes, "+
				"underscores) — shell metacharacters are rejected.",
		),
		mcp.WithString("crew_id",
			mcp.Description("The firstmate crew ID, e.g. 'fc-crew-control-mcp' or 'todo-add-skill'."),
			mcp.Required(),
		),
	)...)

// backlogTool returns the in-flight and queued backlog items.
var backlogTool = mcp.NewTool(ToolBacklog,
	append(readOnlyOpts(),
		mcp.WithDescription(
			"Show the in-flight and queued backlog items from the firstmate backlog. "+
				"This is a READ-ONLY tool — it does not add, remove, or modify backlog items.",
		),
	)...)

// --- Handlers -------------------------------------------------------------

// fleetOverviewHandler returns an MCP handler that runs fm-fleet-view.sh.
func fleetOverviewHandler(c *Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		output, err := c.FleetOverview(ctx)
		if err != nil {
			return toolError("fleet_overview failed", err), nil
		}
		return mcp.NewToolResultText(output), nil
	}
}

// crewStateHandler returns an MCP handler that runs fm-crew-state.sh for the
// validated crew ID.
func crewStateHandler(c *Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args crewStateArgs
		if err := req.BindArguments(&args); err != nil {
			return mcp.NewToolResultErrorFromErr("invalid arguments: %w", err), nil
		}
		state, err := c.CrewState(ctx, args.CrewID)
		if err != nil {
			return toolError("crew_state failed", err), nil
		}
		return mcp.NewToolResultText(state), nil
	}
}

// backlogHandler returns an MCP handler that reads and parses backlog.md.
func backlogHandler(c *Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		output, err := c.Backlog(ctx)
		if err != nil {
			return toolError("backlog failed", err), nil
		}
		return mcp.NewToolResultText(output), nil
	}
}

// toolError returns an IsError result describing a tool failure.
func toolError(action string, err error) *mcp.CallToolResult {
	return mcp.NewToolResultErrorFromErr(action, err)
}

// NewMCPServer builds an MCPServer with all three read-only crew-control
// tools, backed by the given Client.
func NewMCPServer(c *Client) *server.MCPServer {
	srv := server.NewMCPServer("crew-control", "0.1.0")
	srv.AddTool(fleetOverviewTool, fleetOverviewHandler(c))
	srv.AddTool(crewStateTool, crewStateHandler(c))
	srv.AddTool(backlogTool, backlogHandler(c))
	return srv
}

// isReadOnlyTool reports whether a tool name is in the read-only surface.
// This is used by tests to assert no write tools are registered.
func isReadOnlyTool(name string) bool {
	switch name {
	case ToolFleetOverview, ToolCrewState, ToolBacklog:
		return true
	default:
		return false
	}
}

// AllToolNames returns the canonical list of tool names exposed by this addon.
func AllToolNames() []string {
	return []string{ToolFleetOverview, ToolCrewState, ToolBacklog}
}

// containsWriteTool reports whether the given list of names includes any
// tool that is NOT in the read-only surface.
func containsWriteTool(names []string) bool {
	for _, n := range names {
		if !isReadOnlyTool(n) {
			return true
		}
	}
	return false
}

// writeToolKeywords checks whether any name suggests mutation (start, stop,
// steer, teardown, etc.) — used as a secondary guard in tests.
var writeToolKeywords = []string{"start", "stop", "steer", "teardown",
	"remove", "delete", "create", "add", "edit", "move", "cancel", "restart",
	"scale", "deploy", "kill", "pause", "resume", "suspend"}

func nameSuggestsWrite(name string) bool {
	lower := strings.ToLower(name)
	for _, kw := range writeToolKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
