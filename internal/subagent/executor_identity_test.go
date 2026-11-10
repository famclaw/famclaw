package subagent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

func TestDispatchSubagentToolPreservesExactMCPIdentity(t *testing.T) {
	for _, rawReadOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "mutable MCP", true: "readonly MCP"}[rawReadOnly], func(t *testing.T) {
			server := mcpsrv.NewMCPServer("fixture", "1")
			calls := 0
			server.AddTool(mcpgo.NewTool("web_search", mcpgo.WithReadOnlyHintAnnotation(rawReadOnly)), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				calls++
				return mcpgo.NewToolResultError("failure"), nil
			})
			pool := mcp.NewPool("", false, false)
			if err := pool.RegisterInProcessServer("fixture", server); err != nil {
				t.Fatal(err)
			}
			defer pool.StopAll()
			defs, allowed, readonly := filterTools(pool.ListToolInfos(), []string{"web_search"}, nil)
			builtinDefs, builtins := filterBuiltinDefs([]llm.ToolDef{{Function: llm.ToolDefFunc{Name: "builtin__web_search"}}}, []string{"web_search"}, nil)
			if len(defs) != 1 || len(builtinDefs) != 1 || defs[0].Function.Name != "web_search" || builtinDefs[0].Function.Name != "builtin__web_search" || !allowed["web_search"] || !builtins["builtin__web_search"] {
				t.Fatal("real filters did not admit both tools")
			}
			builtinCalls := 0
			deps := ExecutorDeps{Pool: pool, BuiltinReadOnly: map[string]bool{"builtin__web_search": !rawReadOnly, "web_search": !rawReadOnly}, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
				builtinCalls++
				return "", errors.New("failure")
			}}
			out, ok := dispatchSubagentTool(context.Background(), llm.ToolCall{Function: llm.ToolCallFunction{Name: "web_search"}}, deps, allowed, builtins, readonly, time.Now())
			want := "changes may"
			if rawReadOnly {
				want = "No changes were made"
			}
			if !ok || calls != 1 || builtinCalls != 0 || !strings.Contains(out, want) {
				t.Errorf("requested raw MCP web_search, got MCP calls=%d builtin calls=%d display=%q", calls, builtinCalls, out)
			}
		})
	}
}

func TestDispatchSubagentToolIdentityPermissions(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		allow, deny          []string
		requested            string
		wantMCP, wantBuiltin int
		wantOK               bool
	}{
		{"both qualified builtin", []string{"web_search"}, nil, "builtin__web_search", 0, 1, true},
		{"builtin-only bare fallback", []string{"builtin__web_search"}, nil, "web_search", 0, 1, true},
		{"builtin denied raw MCP allowed", []string{"web_search"}, []string{"builtin__web_search"}, "web_search", 1, 0, true},
		{"both denied", []string{"web_search"}, []string{"web_search"}, "web_search", 0, 0, false},
		{"default deny", nil, nil, "web_search", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := mcpsrv.NewMCPServer("fixture", "1")
			calls := 0
			server.AddTool(mcpgo.NewTool("web_search"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				calls++
				return mcpgo.NewToolResultText("mcp"), nil
			})
			pool := mcp.NewPool("", false, false)
			if err := pool.RegisterInProcessServer("fixture", server); err != nil {
				t.Fatal(err)
			}
			defer pool.StopAll()
			_, allowed, readonly := filterTools(pool.ListToolInfos(), tc.allow, tc.deny)
			_, builtins := filterBuiltinDefs([]llm.ToolDef{{Function: llm.ToolDefFunc{Name: "builtin__web_search"}}}, tc.allow, tc.deny)
			builtinCalls := 0
			deps := ExecutorDeps{Pool: pool, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) { builtinCalls++; return "builtin", nil }}
			_, ok := dispatchSubagentTool(context.Background(), llm.ToolCall{Function: llm.ToolCallFunction{Name: tc.requested}}, deps, allowed, builtins, readonly, time.Now())
			if ok != tc.wantOK || calls != tc.wantMCP || builtinCalls != tc.wantBuiltin {
				t.Errorf("ok=%v MCP=%d builtin=%d; want %v/%d/%d", ok, calls, builtinCalls, tc.wantOK, tc.wantMCP, tc.wantBuiltin)
			}
		})
	}
}

func TestDispatchSubagentToolUnavailableRawDoesNotFallback(t *testing.T) {
	pool := mcp.NewPool("", false, false)
	defer pool.StopAll()
	builtinCalls := 0
	deps := ExecutorDeps{Pool: pool, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) { builtinCalls++; return "builtin", nil }}
	_, ok := dispatchSubagentTool(context.Background(), llm.ToolCall{Function: llm.ToolCallFunction{Name: "web_search"}}, deps, map[string]bool{"web_search": true}, map[string]bool{"builtin__web_search": true}, nil, time.Now())
	if ok || builtinCalls != 0 {
		t.Errorf("offered but unavailable raw tool fell back: ok=%v builtin=%d", ok, builtinCalls)
	}
}
