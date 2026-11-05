package mcp

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// TestListToolInfosSurfacesReadOnlyHint: the pool must surface a
// server's readOnlyHint annotation on ToolInfo. That field is the data
// source for the read-only failure-display classification in both the
// main tool loop (agentcore.turnReadOnly / outcomeWarning, via
// turn.Tools) and the subagent executor (filterTools). Absent or
// explicitly-false hints must stay false so the conservative
// "changes may have occurred" display is preserved.
func TestListToolInfosSurfacesReadOnlyHint(t *testing.T) {
	ok := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
	srv := server.NewMCPServer("hint-test", "1")
	srv.AddTool(mcp.NewTool("readonly_lookup", mcp.WithReadOnlyHintAnnotation(true)), ok)
	srv.AddTool(mcp.NewTool("mutating_write"), ok)
	srv.AddTool(mcp.NewTool("explicit_false_hint", mcp.WithReadOnlyHintAnnotation(false)), ok)

	pool := NewPool("", false, false)
	if err := pool.RegisterInProcessServer("hint-test", srv); err != nil {
		t.Fatalf("RegisterInProcessServer: %v", err)
	}

	got := map[string]bool{}
	for _, info := range pool.ListToolInfos() {
		got[info.Name] = info.ReadOnly
	}
	if !got["readonly_lookup"] {
		t.Errorf("readonly_lookup ReadOnly = false, want true (readOnlyHint=true)")
	}
	if got["mutating_write"] {
		t.Errorf("mutating_write ReadOnly = true, want false (no hint present)")
	}
	if got["explicit_false_hint"] {
		t.Errorf("explicit_false_hint ReadOnly = true, want false (readOnlyHint=false)")
	}
}
