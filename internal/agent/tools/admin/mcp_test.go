package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// mockMCPManager is a no-op MCPManager for testing mcp_add/mcp_list.
type mockMCPManager struct {
	reloaded  bool
	lastCfg   *config.Config
	reloadErr error
}

func (m *mockMCPManager) ReloadConfig(cfg *config.Config) error {
	m.reloaded = true
	m.lastCfg = cfg
	return m.reloadErr
}

func newMCPTestDeps(t *testing.T) Deps {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return Deps{
		DB:    db,
		Actor: "dep",
		Cfg: &config.Config{
			Users: []config.UserConfig{
				{Name: "dep", Role: "parent"},
			},
			Skills: config.SkillsConfig{
				MCPServers: map[string]config.MCPServerConfig{},
			},
		},
		ConfigPath: "", // no persistence in tests by default
	}
}

func TestHandleMCPList(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(deps *Deps)
		wantSub string
		wantErr bool
	}{
		{
			name:    "empty servers returns no servers message",
			setup:   func(d *Deps) { d.Cfg.Skills.MCPServers = nil },
			wantSub: "No MCP servers configured.",
		},
		{
			name: "single stdio server listed",
			setup: func(d *Deps) {
				d.Cfg.Skills.MCPServers = map[string]config.MCPServerConfig{
					"playwright": {Transport: "stdio", Command: "npx", Args: []string{"-y", "server"}},
				}
			},
			wantSub: "playwright",
		},
		{
			name: "multiple servers sorted alphabetically",
			setup: func(d *Deps) {
				d.Cfg.Skills.MCPServers = map[string]config.MCPServerConfig{
					"zebra":  {Transport: "http", URL: "http://localhost:8080"},
					"alpha":  {Transport: "stdio", Command: "npx", Args: []string{"server"}},
					"midway": {Transport: "sse", URL: "http://localhost:9090/sse"},
				}
			},
			wantSub: "alpha",
		},
		{
			name: "disabled server shows disabled status",
			setup: func(d *Deps) {
				d.Cfg.Skills.MCPServers = map[string]config.MCPServerConfig{
					"old": {Transport: "stdio", Command: "npx", Disabled: true},
				}
			},
			wantSub: "disabled",
		},
		{
			name:    "nil config returns error",
			setup:   func(d *Deps) { d.Cfg = nil },
			wantSub: "",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newMCPTestDeps(t)
			if tc.setup != nil {
				tc.setup(&deps)
			}
			out, err := HandleMCPList(context.Background(), deps, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got out=%q", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if tc.wantSub != "" && !strings.Contains(out, tc.wantSub) {
				t.Errorf("output %q missing %q", out, tc.wantSub)
			}
		})
	}
}

func TestHandleMCPAdd(t *testing.T) {
	cases := []struct {
		name         string
		args         map[string]any
		setup        func(deps *Deps)
		wantSub      string
		wantErr      bool
		expectReload bool
	}{
		{
			name: "stdio happy path with string args",
			args: map[string]any{
				"name":    "playwright",
				"command": "npx",
				"args":    "-y @modelcontextprotocol/server-playwright",
			},
			wantSub:      "registered",
			expectReload: true,
		},
		{
			name: "http transport via url",
			args: map[string]any{
				"name": "myserver",
				"url":  "http://localhost:8080/mcp",
			},
			wantSub:      "registered",
			expectReload: true,
		},
		{
			name: "explicit transport field",
			args: map[string]any{
				"name":      "sse",
				"url":       "http://localhost:9090/sse",
				"transport": "sse",
			},
			wantSub:      "registered",
			expectReload: true,
		},
		{
			name: "duplicate name rejected",
			args: map[string]any{
				"name":    "existing",
				"command": "npx",
				"args":    "server",
			},
			setup: func(d *Deps) {
				d.Cfg.Skills.MCPServers = map[string]config.MCPServerConfig{
					"existing": {Transport: "stdio", Command: "npx", Args: []string{"server"}},
				}
			},
			wantSub: "already exists",
			wantErr: true,
		},
		{
			name:    "missing name rejected",
			args:    map[string]any{"command": "npx"},
			wantErr: true,
		},
		{
			name:    "empty name rejected",
			args:    map[string]any{"name": "  ", "command": "npx"},
			wantErr: true,
		},
		{
			name:    "stdio without command rejected",
			args:    map[string]any{"name": "badstdio", "transport": "stdio"},
			wantErr: true,
		},
		{
			name:    "no command or url rejected",
			args:    map[string]any{"name": "notransport"},
			wantErr: true,
		},
		{
			name:    "nil config returns error",
			args:    map[string]any{"name": "x", "command": "npx"},
			setup:   func(d *Deps) { d.Cfg = nil },
			wantErr: true,
		},
		{
			name: "args as array supported",
			args: map[string]any{
				"name":    "arrargs",
				"command": "npx",
				"args":    []any{"-y", "server-playwright"},
			},
			wantSub:      "registered",
			expectReload: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newMCPTestDeps(t)
			if tc.setup != nil {
				tc.setup(&deps)
			}

			mock := &mockMCPManager{}
			if deps.Cfg != nil {
				deps.MCP = mock
			}

			out, err := HandleMCPAdd(context.Background(), deps, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got out=%q", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if tc.wantSub != "" && !strings.Contains(out, tc.wantSub) {
				t.Errorf("output %q missing %q", out, tc.wantSub)
			}
			if tc.expectReload && !mock.reloaded {
				t.Error("expected pool to be reloaded, but it was not")
			}
		})
	}
}

func TestHandleMCPAdd_NilPoolSkipsReload(t *testing.T) {
	deps := newMCPTestDeps(t)
	out, err := HandleMCPAdd(context.Background(), deps, map[string]any{
		"name":    "testsrv",
		"command": "npx",
		"args":    "test",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "registered") {
		t.Errorf("output %q missing 'registered'", out)
	}
	if _, ok := deps.Cfg.Skills.MCPServers["testsrv"]; !ok {
		t.Error("server not added to config when pool was nil")
	}
}

func TestMCPAddAndListDefinitions(t *testing.T) {
	addDef := MCPAddDefinition()
	if addDef.Name != "builtin__mcp_add" {
		t.Errorf("expected builtin__mcp_add, got %s", addDef.Name)
	}
	if len(addDef.Roles) != 1 || addDef.Roles[0] != "parent" {
		t.Errorf("expected parent-only role, got %v", addDef.Roles)
	}
	if addDef.Source != "builtin" {
		t.Errorf("expected builtin source, got %s", addDef.Source)
	}

	listDef := MCPListDefinition()
	if listDef.Name != "builtin__mcp_list" {
		t.Errorf("expected builtin__mcp_list, got %s", listDef.Name)
	}
	if len(listDef.Roles) != 1 || listDef.Roles[0] != "parent" {
		t.Errorf("expected parent-only role, got %v", listDef.Roles)
	}
}
