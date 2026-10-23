package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

func initPolicy(shellEnabled bool) *PolicyConfig {
	return &PolicyConfig{
		DeviceAlias:  "test-device",
		ScreenPolicy: "refuse_when_off",
		Tools: ToolFlags{
			Shell: shellEnabled,
		},
	}
}

// inProcessClient spins up a server, initializes an in-process client, and
// returns it for ListTools/CallTool assertions.
func inProcessClient(t *testing.T, policy *PolicyConfig) *client.Client {
	t.Helper()
	srv := New(policy, "test")
	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	return initInProcessClient(t, c)
}

// inProcessClientWithExecutor builds the in-process client against a
// fake-adb-backed executor (design §6.1 fake-adb seam) so dispatch tests
// never touch a real device.
func inProcessClientWithExecutor(t *testing.T, policy *PolicyConfig, adb *ADBExecutor) *client.Client {
	t.Helper()
	srv := NewWithExecutor(policy, "test", adb)
	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	return initInProcessClient(t, c)
}

func initInProcessClient(t *testing.T, c *client.Client) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "0.0.1"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return c
}

func TestToolList_ShellOff(t *testing.T) {
	c := inProcessClient(t, initPolicy(false))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := mcp.ListToolsRequest{}
	res, err := c.ListTools(ctx, req)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	tools := res.Tools
	if len(tools) != 8 {
		t.Fatalf("want 8 tools, got %d: %v", len(tools), toolNames(tools))
	}
	want := map[string]bool{
		"device.status": true, "screenshot": true, "find": true,
		"tap": true, "type": true, "keyevent": true,
		"swipe": true, "open_app": true,
	}
	got := make(map[string]bool, len(tools))
	for _, tl := range tools {
		got[tl.Name] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("expected tool %q missing", name)
		}
	}
	if got["shell"] {
		t.Errorf("shell tool should NOT be present when shell is disabled")
	}
}

func TestToolList_ShellOn(t *testing.T) {
	c := inProcessClient(t, initPolicy(true))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := mcp.ListToolsRequest{}
	res, err := c.ListTools(ctx, req)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != 9 {
		t.Fatalf("want 9 tools (8 + shell), got %d", len(res.Tools))
	}
}

func TestDispatch_ReturnsNotImplemented(t *testing.T) {
	c := inProcessClient(t, initPolicy(false))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// tap is a mutating tool: still a stub until the policy/safety card, so
	// it exercises the not-implemented path without touching adb.
	req := mcp.CallToolRequest{}
	req.Params.Name = "tap"
	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Errorf("expected success result, got error: %+v", res)
	}
	text := resultText(res)
	if !strings.Contains(text, "not implemented") {
		t.Errorf("expected 'not implemented' in result, got: %q", text)
	}
}

func toolNames(tools []mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return names
}

func resultText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}
