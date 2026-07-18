package gateway

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestHandleMCPCommand(t *testing.T) {
	// Create a temporary directory for the config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Create a test config with a parent and a child user
	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "parent", Role: "parent", PIN: "1234"},
			{Name: "child", Role: "child", PIN: "5678"},
		},
		Skills: config.SkillsConfig{
			MCPServers: make(map[string]config.MCPServerConfig),
		},
	}

	// Create a router with the config and a nil registry (not used in MCP commands)
	identStore := &identity.Store{}
	db := &store.DB{}
	notifier := &notify.MultiNotifier{}
	router := NewRouter(context.TODO(), cfg, identStore, nil, nil, db, notifier, nil, nil, configPath)

	// Helper to create an adjustedUser from a user name
	getUser := func(name string) *config.UserConfig {
		u := cfg.GetUser(name)
		if u == nil {
			t.Fatalf("user %q not found", name)
		}
		return u
	}

	testCases := []struct {
		name       string
		userName   string
		fields     []string
		wantText   string
		wantPolicy string
		wantErr    bool // whether we expect an error in the reply text (PolicyAction: "error")
	}{
		{
			name:       "list - no servers - parent",
			userName:   "parent",
			fields:     []string{"mcp", "list"},
			wantText:   "No MCP servers configured.",
			wantPolicy: "mcp",
		},
		{
			name:       "list - no servers - child",
			userName:   "child",
			fields:     []string{"mcp", "list"},
			wantText:   "Only a parent can manage MCP servers.",
			wantPolicy: "block",
		},
		{
			name:       "add stdio server - parent",
			userName:   "parent",
			fields:     []string{"mcp", "add", "test-stdio", "stdio", "command=/bin/echo", "args=hello,world"},
			wantText:   "MCP server \"test-stdio\" added.",
			wantPolicy: "mcp",
		},
		{
			name:       "add stdio server - child (should be blocked)",
			userName:   "child",
			fields:     []string{"mcp", "add", "test-stdio2", "stdio", "command=/bin/echo"},
			wantText:   "Only a parent can manage MCP servers.",
			wantPolicy: "block",
		},
		{
			name:       "add http server - parent",
			userName:   "parent",
			fields:     []string{"mcp", "add", "test-http", "http", "url=http://example.com"},
			wantText:   "MCP server \"test-http\" added.",
			wantPolicy: "mcp",
		},
		{
			name:       "add http server - child (should be blocked)",
			userName:   "child",
			fields:     []string{"mcp", "add", "test-http2", "http", "url=http://example.com"},
			wantText:   "Only a parent can manage MCP servers.",
			wantPolicy: "block",
		},
		{
			name:       "remove server - parent",
			userName:   "parent",
			fields:     []string{"mcp", "remove", "test-stdio"},
			wantText:   "MCP server \"test-stdio\" removed.",
			wantPolicy: "mcp",
		},
		{
			name:       "remove server - child (should be blocked)",
			userName:   "child",
			fields:     []string{"mcp", "remove", "test-http"},
			wantText:   "Only a parent can manage MCP servers.",
			wantPolicy: "block",
		},
		{
			name:       "add invalid stdio (missing command) - parent",
			userName:   "parent",
			fields:     []string{"mcp", "add", "bad-stdio", "stdio"},
			wantText:   "Invalid MCP server config: MCP server \"bad-stdio\": stdio transport requires command",
			wantPolicy: "error",
		},
		{
			name:       "add invalid transport - parent",
			userName:   "parent",
			fields:     []string{"mcp", "add", "bad-transport", "invalid"},
			wantText:   "Invalid MCP server config: MCP server \"bad-transport\": unknown transport \"invalid\" (use stdio, http, or sse)",
			wantPolicy: "error",
		},
		{
			name:       "remove non-existent server - parent",
			userName:   "parent",
			fields:     []string{"mcp", "remove", "non-existent"},
			wantText:   "MCP server \"non-existent\" removed.",
			wantPolicy: "mcp",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			user := getUser(tc.userName)
			reply := router.handleMCPCommand(context.TODO(), user.Name, tc.fields)
			if tc.wantErr {
				require.Equal(t, "error", reply.PolicyAction, "expected error policy action for %s", tc.name)
			} else {
				require.Equal(t, tc.wantPolicy, reply.PolicyAction, "unexpected policy action for %s", tc.name)
			}
			require.Contains(t, reply.Text, tc.wantText, "unexpected reply text for %s\n got: %q\n want substring: %q", tc.name, reply.Text, tc.wantText)
		})
	}
}

func TestHandleMCPCommand_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "parent", Role: "parent", PIN: "1234"},
		},
		Skills: config.SkillsConfig{
			MCPServers: make(map[string]config.MCPServerConfig),
		},
	}

	identStore := &identity.Store{}
	db := &store.DB{}
	notifier := &notify.MultiNotifier{}
	router := NewRouter(context.TODO(), cfg, identStore, nil, nil, db, notifier, nil, nil, configPath)

	parent := getUser(t, cfg, "parent")

	// Add a server
	reply := router.handleMCPCommand(context.TODO(), parent.Name, []string{"mcp", "add", "test-persist", "stdio", "command=/bin/ls"})
	require.Equal(t, "mcp", reply.PolicyAction)
	require.Contains(t, reply.Text, "MCP server \"test-persist\" added.")

	// Check that the config file was created and contains the server
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	// Unmarshal the config to verify
	var loaded config.Config
	err = yaml.Unmarshal(data, &loaded)
	require.NoError(t, err)
	require.Len(t, loaded.Skills.MCPServers, 1)
	server, ok := loaded.Skills.MCPServers["test-persist"]
	require.True(t, ok)
	require.Equal(t, "stdio", server.Transport)
	require.Equal(t, "/bin/ls", server.Command)

	// Remove the server
	reply = router.handleMCPCommand(context.TODO(), parent.Name, []string{"mcp", "remove", "test-persist"})
	require.Equal(t, "mcp", reply.PolicyAction)
	require.Contains(t, reply.Text, "MCP server \"test-persist\" removed.")

	// Check that the config file now has an empty MCPServers map
	fmt.Printf("Reading config from: %s\\n", configPath)
	data, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	fmt.Printf("Config file content: %s\\n", string(data))
	err = yaml.Unmarshal(data, &loaded)
	if err != nil {
		t.Fatalf("failed to unmarshal config: %v", err)
	}
	if len(loaded.Skills.MCPServers) != 0 {
		t.Fatalf("Expected empty MCPServers, got: %v", loaded.Skills.MCPServers)
	}
}

// getUser is a helper to get a user config by name from the test config.
func getUser(t *testing.T, cfg *config.Config, name string) *config.UserConfig {
	t.Helper()
	u := cfg.GetUser(name)
	if u == nil {
		t.Fatalf("user %q not found", name)
	}
	return u
}
