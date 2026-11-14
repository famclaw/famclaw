package agentcore

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpgoserver "github.com/mark3labs/mcp-go/server"
)

// identityFixture uses the same in-process pool and metadata exposed to the
// real agent while keeping both the model and tools entirely local.
func identityFixture(t *testing.T, mcpName string, mcpReadOnly, mcpFails bool) (*mcp.Pool, Tool, *int) {
	t.Helper()
	calls := new(int)
	srv := mcpgoserver.NewMCPServer("fixture", "1")
	srv.AddTool(mcpgo.NewTool(mcpName, mcpgo.WithReadOnlyHintAnnotation(mcpReadOnly)),
		func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			*calls++
			if mcpFails {
				return mcpgo.NewToolResultError("synthetic failure"), nil
			}
			return mcpgo.NewToolResultText("mcp result"), nil
		})
	pool := mcp.NewPool("", false, false)
	if err := pool.RegisterInProcessServer("fixture", srv); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.StopAll)
	infos := pool.ListToolInfos()
	if len(infos) != 1 || infos[0].Name != mcpName || infos[0].ReadOnly != mcpReadOnly {
		t.Fatalf("unexpected MCP metadata: %+v", infos)
	}
	return pool, Tool{Name: infos[0].Name, Source: "mcp", ReadOnly: infos[0].ReadOnly}, calls
}

func runIdentityCall(t *testing.T, pool *mcp.Pool, tools []Tool, name string, builtinFails bool) (*Turn, *bareNameMockChatter, int) {
	t.Helper()
	builtinCalls := 0
	chatter := &bareNameMockChatter{responses: []llm.Message{{Content: "done"}}}
	deps := ToolLoopDeps{Pool: pool, MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter },
		BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
			builtinCalls++
			if builtinFails {
				return "", errors.New("synthetic failure")
			}
			return "builtin result", nil
		}}
	turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "parent"}, Tools: tools}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "call", Function: llm.ToolCallFunction{Name: name, Arguments: map[string]any{}}}})
	turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
	if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	return turn, chatter, builtinCalls
}

func TestStageToolLoopMutationIdentityCollision(t *testing.T) {
	for _, tc := range []struct {
		name, mcpName, requested     string
		mcpReadOnly, builtinReadOnly bool
		useBuiltin                   bool
	}{
		{"mutating builtin and readonly raw MCP", "shared", "builtin__shared", true, false, true},
		{"mutating qualified MCP and readonly builtin", "mcp__server__shared", "mcp__server__shared", false, true, false},
	} {
		for _, reverse := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/mcp-first", true: "/builtin-first"}[reverse], func(t *testing.T) {
				pool, mcpTool, mcpCalls := identityFixture(t, tc.mcpName, tc.mcpReadOnly, !tc.useBuiltin)
				builtin := Tool{Name: "builtin__shared", Source: "builtin", ReadOnly: tc.builtinReadOnly}
				tools := []Tool{mcpTool, builtin}
				if reverse {
					tools[0], tools[1] = tools[1], tools[0]
				}
				turn, chatter, builtinCalls := runIdentityCall(t, pool, tools, tc.requested, tc.useBuiltin)
				if tc.useBuiltin && (builtinCalls != 1 || *mcpCalls != 0) {
					t.Fatalf("builtin=%d mcp=%d", builtinCalls, *mcpCalls)
				}
				if !tc.useBuiltin && (builtinCalls != 0 || *mcpCalls != 1) {
					t.Fatalf("builtin=%d mcp=%d", builtinCalls, *mcpCalls)
				}
				if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Disposition != DispositionAttempted || turn.ToolCalls[0].Outcome != OutcomeFailure {
					t.Fatalf("unexpected results: %+v", turn.ToolCalls)
				}
				var perCall string
				for _, msg := range chatter.messages {
					if msg.Role == "tool" && msg.ToolCallID == "call" {
						perCall = msg.Content
					}
				}
				if perCall != ToolErrorDisplayChangesMayHaveOccurred {
					t.Errorf("per-call display = %q, want conservative warning", perCall)
				}
				if !strings.Contains(turn.Output, "changes may still have occurred") || strings.Contains(turn.Output, "No changes were made") {
					t.Errorf("final warning = %q, want conservative mutation warning", turn.Output)
				}
			})
		}
	}
}

func TestStageToolLoopExactRawMCPNameBeforeBuiltinFallback(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "mcp-first", true: "builtin-first"}[reverse], func(t *testing.T) {
			pool, mcpTool, mcpCalls := identityFixture(t, "shared", false, false)
			builtin := Tool{Name: "builtin__shared", Source: "builtin", ReadOnly: true}
			tools := []Tool{mcpTool, builtin}
			if reverse {
				tools[0], tools[1] = tools[1], tools[0]
			}
			turn, _, builtinCalls := runIdentityCall(t, pool, tools, "shared", false)
			if builtinCalls != 0 || *mcpCalls != 1 {
				t.Errorf("raw MCP request dispatched builtin=%d mcp=%d, want builtin=0 mcp=1", builtinCalls, *mcpCalls)
			}
			if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].ToolName != "shared" || turn.ToolCalls[0].Output != "mcp result" {
				t.Errorf("raw MCP request result: %+v", turn.ToolCalls)
			}
		})
	}
}

func TestOutcomeWarningDoesNotInheritReadOnlyFromOtherIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, called string
		tools        []Tool
	}{
		{"builtin mutation versus raw readonly", "builtin__shared", []Tool{{Name: "shared", Source: "mcp", ReadOnly: true}, {Name: "builtin__shared", Source: "builtin"}}},
		{"qualified MCP mutation versus builtin readonly", "mcp__server__shared", []Tool{{Name: "builtin__shared", Source: "builtin", ReadOnly: true}, {Name: "mcp__server__shared", Source: "mcp"}}},
	} {
		for _, reverse := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/first", true: "/reversed"}[reverse], func(t *testing.T) {
				tools := append([]Tool(nil), tc.tools...)
				if reverse {
					tools[0], tools[1] = tools[1], tools[0]
				}
				got := outcomeWarning([]ToolResult{{ToolName: tc.called, Disposition: DispositionAttempted, Outcome: OutcomeFailure}}, tools)
				if !strings.Contains(got, "changes may still have occurred") || strings.Contains(got, "No changes were made") {
					t.Errorf("warning = %q, want mutation warning", got)
				}
			})
		}
	}
}

func TestOutcomeWarningAmbiguousBareAliasStaysConservative(t *testing.T) {
	tools := []Tool{
		{Name: "mcp__first__shared", Source: "mcp", ReadOnly: true},
		{Name: "mcp__second__shared", Source: "mcp"},
	}
	for _, outcome := range []ExecutionOutcome{OutcomeFailure, OutcomeUnknown} {
		got := outcomeWarning([]ToolResult{{ToolName: "shared", Disposition: DispositionAttempted, Outcome: outcome}}, tools)
		if !strings.Contains(got, "changes may") || strings.Contains(got, "no changes were made") || strings.Contains(got, "No changes were made") {
			t.Errorf("outcome %v: warning = %q, want conservative warning for ambiguous alias", outcome, got)
		}
	}
}

func TestToolsToLLMDefsDisambiguatesBuiltinAndRawMCP(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "mcp-first", true: "builtin-first"}[reverse], func(t *testing.T) {
			tools := []Tool{{Name: "shared", Source: "mcp"}, {Name: "builtin__shared", Source: "builtin"}}
			if reverse {
				tools[0], tools[1] = tools[1], tools[0]
			}
			defs := toolsToLLMDefs(tools)
			names := map[string]int{}
			for _, def := range defs {
				names[def.Function.Name]++
			}
			if names["shared"] != 1 || names["builtin__shared"] != 1 || len(names) != 2 {
				t.Errorf("advertised names = %v, want distinct raw MCP and qualified builtin", names)
			}
		})
	}
}

func TestToolsToLLMDefsKeepsUnambiguousBuiltinBare(t *testing.T) {
	defs := toolsToLLMDefs([]Tool{{Name: "builtin__shared", Source: "builtin"}})
	if len(defs) != 1 || defs[0].Function.Name != "shared" {
		t.Fatalf("unambiguous advertised builtin = %+v", defs)
	}
}

func TestBuildReadOnlyIndexIdentityResolution(t *testing.T) {
	cases := []struct {
		name  string
		tools []Tool
		probe string
		want  bool
	}{
		{"exact readonly wins over mutable sibling", []Tool{{Name: "shared", ReadOnly: true}, {Name: "builtin__shared"}}, "shared", true},
		{"exact mutable wins over readonly sibling", []Tool{{Name: "shared", ReadOnly: true}, {Name: "builtin__shared"}}, "builtin__shared", false},
		{"unambiguous builtin bare alias", []Tool{{Name: "builtin__shared", ReadOnly: true}}, "shared", true},
		{"unambiguous MCP bare alias", []Tool{{Name: "mcp__server__shared", ReadOnly: true}}, "shared", true},
		{"nested builtin prefix is not an alias", []Tool{{Name: "mcp__server__builtin__shared", ReadOnly: true}}, "builtin__shared", false},
		{"nested MCP prefix is not an alias", []Tool{{Name: "mcp__server__mcp__shared", ReadOnly: true}}, "mcp__shared", false},
		{"unknown qualified identity stays conservative", []Tool{{Name: "builtin__shared", ReadOnly: true}}, "mcp__other__shared", false},
		{"ambiguous bare alias stays conservative", []Tool{{Name: "mcp__first__shared", ReadOnly: true}, {Name: "mcp__second__shared", ReadOnly: true}}, "shared", false},
		{"duplicate exact declarations stay conservative", []Tool{{Name: "shared", ReadOnly: true}, {Name: "shared"}}, "shared", false},
	}
	for _, tc := range cases {
		for _, reverse := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/first", true: "/reversed"}[reverse], func(t *testing.T) {
				tools := append([]Tool(nil), tc.tools...)
				if reverse && len(tools) == 2 {
					tools[0], tools[1] = tools[1], tools[0]
				}
				if got := turnReadOnlyLookup(buildReadOnlyIndex(tools), tc.probe); got != tc.want {
					t.Errorf("probe %q = %v, want %v", tc.probe, got, tc.want)
				}
			})
		}
	}
}

// The child runs through the real embedded policy and a temporary SQLite store.
// Both implementations have the same policy category but different identities.
func TestToolApprovalIdentityIsolation(t *testing.T) {
	for _, first := range []string{"builtin__file_write", "file_write"} {
		t.Run(first, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(filepath.Join(t.TempDir(), "approval.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			evaluator, err := policy.NewEvaluator("", "", "")
			if err != nil {
				t.Fatal(err)
			}
			pool, mcpTool, mcpCalls := identityFixture(t, "file_write", false, false)
			builtinCalls := 0
			deps := ToolLoopDeps{Pool: pool, PolicyEvaluator: evaluator, ApprovalStore: db, MaxIterations: 1,
				ClientFactory: func(*Turn) llm.Chatter { return &bareNameMockChatter{responses: []llm.Message{{Content: "done"}}} },
				BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
					builtinCalls++
					return "builtin result", nil
				},
			}
			args := map[string]any{"path": "test.sh", "content": "#!/bin/sh\necho hi"}
			run := func(name string) *Turn {
				turn := &Turn{User: &config.UserConfig{Name: "kid", Role: "child", AgeGroup: "age_8_12"}, Tools: []Tool{mcpTool, {Name: "builtin__file_write", Source: "builtin"}}}
				turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "call", Function: llm.ToolCallFunction{Name: name, Arguments: args}}})
				turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
				if err := NewStageToolLoop(deps)(ctx, turn); err != nil {
					t.Fatal(err)
				}
				return turn
			}
			a := run(first)
			if len(a.ToolCalls) != 1 || !errors.Is(a.ToolCalls[0].Error, ErrApprovalRequested) {
				t.Fatalf("first call = %+v", a.ToolCalls)
			}
			pending, err := db.PendingApprovals(ctx)
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending = %+v, %v", pending, err)
			}
			if pending[0].QueryText != first+" - path test.sh" || pending[0].Category != "tool:file_write" {
				t.Errorf("first approval identity/category = %+v", pending[0])
			}
			if err := db.DecideApproval(pending[0].ID, "approved", "parent"); err != nil {
				t.Fatal(err)
			}
			// The approval must permit its own identity.
			same := run(first)
			if len(same.ToolCalls) != 1 || same.ToolCalls[0].Error != nil {
				t.Fatalf("same identity after approval = %+v", same.ToolCalls)
			}
			beforeBuiltin, beforeMCP := builtinCalls, *mcpCalls
			second := "file_write"
			if first == second {
				second = "builtin__file_write"
			}
			other := run(second)
			if len(other.ToolCalls) != 1 || !errors.Is(other.ToolCalls[0].Error, ErrApprovalRequested) || builtinCalls != beforeBuiltin || *mcpCalls != beforeMCP {
				t.Errorf("approval for %q authorized %q: result=%+v builtin calls %d->%d MCP calls %d->%d", first, second, other.ToolCalls, beforeBuiltin, builtinCalls, beforeMCP, *mcpCalls)
			}
			pending, err = db.PendingApprovals(ctx)
			if err != nil || len(pending) != 1 {
				t.Fatalf("second pending = %+v, %v", pending, err)
			}
			if pending[0].QueryText != second+" - path test.sh" || pending[0].Category != "tool:file_write" {
				t.Errorf("approval identity/category = %+v", pending[0])
			}
		})
	}
}

func TestToolApprovalLegacyDecisions(t *testing.T) {
	for _, status := range []string{"approved", "denied", "pending"} {
		for _, name := range []string{"file_write", "builtin__file_write"} {
			t.Run(status+"/"+name, func(t *testing.T) {
				ctx := context.Background()
				db, err := store.Open(filepath.Join(t.TempDir(), "approval.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				evaluator, err := policy.NewEvaluator("", "", "")
				if err != nil {
					t.Fatal(err)
				}
				pool, mcpTool, mcpCalls := identityFixture(t, "file_write", false, false)
				args := map[string]any{"path": "test.sh", "content": "#!/bin/sh\necho hi"}
				legacy := toolApprovalID("kid", "file_write", args)
				if _, err := db.UpsertApproval(ctx, &store.Approval{ID: legacy, UserName: "kid", Category: "tool:file_write"}); err != nil {
					t.Fatal(err)
				}
				if status != "pending" {
					if err := db.DecideApproval(legacy, status, "parent"); err != nil {
						t.Fatal(err)
					}
				}
				builtinCalls := 0
				deps := ToolLoopDeps{Pool: pool, PolicyEvaluator: evaluator, ApprovalStore: db, MaxIterations: 1,
					ClientFactory: func(*Turn) llm.Chatter { return &bareNameMockChatter{responses: []llm.Message{{Content: "done"}}} },
					BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
						builtinCalls++
						return "builtin result", nil
					},
				}
				turn := &Turn{User: &config.UserConfig{Name: "kid", Role: "child", AgeGroup: "age_8_12"}, Tools: []Tool{mcpTool, {Name: "builtin__file_write", Source: "builtin"}}}
				turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "call", Function: llm.ToolCallFunction{Name: name, Arguments: args}}})
				turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
				if err := NewStageToolLoop(deps)(ctx, turn); err != nil {
					t.Fatal(err)
				}
				want := ErrApprovalRequested
				if status == "denied" {
					want = ErrToolBlocked
				}
				if len(turn.ToolCalls) != 1 || !errors.Is(turn.ToolCalls[0].Error, want) || builtinCalls != 0 || *mcpCalls != 0 {
					t.Errorf("legacy %s: result=%+v builtin=%d MCP=%d, want %v", status, turn.ToolCalls, builtinCalls, *mcpCalls, want)
				}
				if status == "denied" {
					id := "v2-" + toolApprovalID("kid", name, args)
					if _, err := db.UpsertApproval(ctx, &store.Approval{ID: id, UserName: "kid", Category: "tool:file_write"}); err != nil {
						t.Fatal(err)
					}
					if err := db.DecideApproval(id, "approved", "parent"); err != nil {
						t.Fatal(err)
					}
					retry := &Turn{User: turn.User, Tools: turn.Tools}
					retry.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "retry", Function: llm.ToolCallFunction{Name: name, Arguments: args}}})
					retry.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
					if err := NewStageToolLoop(deps)(ctx, retry); err != nil {
						t.Fatal(err)
					}
					if len(retry.ToolCalls) != 1 || !errors.Is(retry.ToolCalls[0].Error, ErrToolBlocked) || builtinCalls != 0 || *mcpCalls != 0 {
						t.Errorf("legacy denial must override v2 grant: %+v", retry.ToolCalls)
					}
					stored, err := db.GetApproval(ctx, id)
					if err != nil || stored == nil || stored.Status != "approved" {
						t.Fatalf("v2 stored decision was changed: %+v %v", stored, err)
					}
					parent := &Turn{User: &config.UserConfig{Name: "kid", Role: "parent"}, Tools: turn.Tools}
					parent.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "parent", Function: llm.ToolCallFunction{Name: name, Arguments: args}}})
					parent.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
					if err := NewStageToolLoop(deps)(ctx, parent); err != nil {
						t.Fatal(err)
					}
					if len(parent.ToolCalls) != 1 || parent.ToolCalls[0].Error != nil {
						t.Errorf("parent policy with legacy denial: %+v", parent.ToolCalls)
					}
				}
			})
		}
	}
}
