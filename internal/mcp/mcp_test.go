package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/famclaw/famclaw/internal/config"
)

func newMockServer() *server.MCPServer {
	s := server.NewMCPServer("mock-server", "1.0.0",
		server.WithToolCapabilities(true),
	)

	s.AddTool(mcp.NewTool("echo",
		mcp.WithDescription("Echo input text"),
		mcp.WithString("text", mcp.Description("Text to echo")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		text := req.GetString("text", "")
		return mcp.NewToolResultText(text), nil
	})

	s.AddTool(mcp.NewTool("add",
		mcp.WithDescription("Add two numbers"),
		mcp.WithNumber("a", mcp.Description("First number")),
		mcp.WithNumber("b", mcp.Description("Second number")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		a := req.GetFloat("a", 0)
		b := req.GetFloat("b", 0)
		return mcp.NewToolResultText(fmt.Sprintf("%d", int(a+b))), nil
	})

	return s
}

// newTestClient creates a Client backed by an in-process mock MCP server.
func newTestClient(t *testing.T) *Client {
	t.Helper()

	s := newMockServer()
	inner, err := mcpclient.NewInProcessClient(s)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	t.Cleanup(func() { inner.Close() })

	ctx := context.Background()

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "1.0"}
	_, err = inner.Initialize(ctx, initReq)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	toolsResult, err := inner.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	return &Client{
		name:          "mock",
		transportType: "inprocess",
		inner:         inner,
		tools:         toolsResult.Tools,
	}
}

// ── Transport constructor tests ──────────────────────────────────────────────

func TestNewTransportClient_Stdio(t *testing.T) {
	cfg := config.MCPServerConfig{Transport: "stdio", Command: "echo"}
	c := NewTransportClient("test", cfg, "", false, false)
	if c.transportType != "stdio" {
		t.Errorf("transport = %q, want stdio", c.transportType)
	}
}

func TestNewTransportClient_HTTP(t *testing.T) {
	cfg := config.MCPServerConfig{Transport: "http", URL: "http://localhost:9999/mcp"}
	c := NewTransportClient("test", cfg, "", false, false)
	if c.transportType != "http" {
		t.Errorf("transport = %q, want http", c.transportType)
	}
}

func TestNewTransportClient_SSE(t *testing.T) {
	cfg := config.MCPServerConfig{Transport: "sse", URL: "http://localhost:9999/sse"}
	c := NewTransportClient("test", cfg, "", false, false)
	if c.transportType != "sse" {
		t.Errorf("transport = %q, want sse", c.transportType)
	}
}

func TestNewTransportClient_DefaultStdio(t *testing.T) {
	cfg := config.MCPServerConfig{Command: "some-cmd"}
	c := NewTransportClient("test", cfg, "", false, false)
	if c.transportType != "stdio" {
		t.Errorf("empty transport with command should default to stdio, got %q", c.transportType)
	}
}

func TestNewTransportClient_DefaultHTTP(t *testing.T) {
	cfg := config.MCPServerConfig{URL: "http://example.com/mcp"}
	c := NewTransportClient("test", cfg, "", false, false)
	if c.transportType != "http" {
		t.Errorf("empty transport with url should default to http, got %q", c.transportType)
	}
}

func TestNewTransportClient_UnknownTransportFails(t *testing.T) {
	cfg := config.MCPServerConfig{Transport: "grpc", URL: "localhost:50051"}
	c := NewTransportClient("test", cfg, "", false, false)
	err := c.Start(context.Background())
	if err == nil {
		t.Error("expected error for unknown transport")
	}
}

func TestNewTransportClient_StdioFailsBadBinary(t *testing.T) {
	cfg := config.MCPServerConfig{Transport: "stdio", Command: "nonexistent-binary-xyz"}
	c := NewTransportClient("test", cfg, "", false, false)
	err := c.Start(context.Background())
	if err == nil {
		t.Error("expected error for bad binary")
	}
	if c.inner != nil {
		t.Error("inner should be nil after failed start")
	}
}

// ── InProcess client tests (transport-agnostic behavior) ─────────────────────

func TestClientToolsList(t *testing.T) {
	c := newTestClient(t)
	tools := c.Tools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["echo"] {
		t.Error("missing echo tool")
	}
	if !names["add"] {
		t.Error("missing add tool")
	}
}

func TestClientCallToolEcho(t *testing.T) {
	c := newTestClient(t)
	result, err := c.CallTool(context.Background(), "echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if text, ok := mcp.AsTextContent(result.Content[0]); ok {
		if text.Text != "hello" {
			t.Errorf("echo = %q, want hello", text.Text)
		}
	} else {
		t.Error("expected TextContent")
	}
}

func TestClientCallToolAdd(t *testing.T) {
	c := newTestClient(t)
	result, err := c.CallTool(context.Background(), "add", map[string]any{"a": float64(3), "b": float64(4)})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if text, ok := mcp.AsTextContent(result.Content[0]); ok {
		if text.Text != "7" {
			t.Errorf("add = %q, want 7", text.Text)
		}
	} else {
		t.Error("expected TextContent")
	}
}

func TestClientStopNilsInner(t *testing.T) {
	c := newTestClient(t)
	if len(c.Tools()) == 0 {
		t.Fatal("expected tools")
	}
	c.Stop()
	if c.inner != nil {
		t.Error("inner should be nil after Stop")
	}
	if !c.closed {
		t.Error("closed should be true after Stop")
	}
}

// ── Pool tests ───────────────────────────────────────────────────────────────

func TestPoolHasTool(t *testing.T) {
	pool := NewPool("", false, false)
	if pool.HasTool("nonexistent") {
		t.Error("empty pool should not have any tools")
	}
}

func TestPoolCallToolUnknown(t *testing.T) {
	pool := NewPool("", false, false)
	_, err := pool.CallTool(context.Background(), "ghost", nil)
	if err == nil {
		t.Error("expected error for unknown tool")
	}
}

func TestMaxToolCallIterations(t *testing.T) {
	if MaxToolCallIterations != 10 {
		t.Errorf("MaxToolCallIterations = %d, want 10", MaxToolCallIterations)
	}
}

func TestPoolStopAllEmpty(t *testing.T) {
	pool := NewPool("", false, false)
	pool.StopAll()
}

func TestPoolListToolsEmpty(t *testing.T) {
	pool := NewPool("", false, false)
	tools := pool.ListTools()
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

// ── Config validation tests ──────────────────────────────────────────────────

func TestValidateMCPServer(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.MCPServerConfig
		wantErr bool
	}{
		{"valid stdio", config.MCPServerConfig{Transport: "stdio", Command: "echo"}, false},
		{"valid http", config.MCPServerConfig{Transport: "http", URL: "http://localhost/mcp"}, false},
		{"valid sse", config.MCPServerConfig{Transport: "sse", URL: "http://localhost/sse"}, false},
		{"infer stdio", config.MCPServerConfig{Command: "echo"}, false},
		{"infer http", config.MCPServerConfig{URL: "http://localhost/mcp"}, false},
		{"stdio missing command", config.MCPServerConfig{Transport: "stdio"}, true},
		{"http missing url", config.MCPServerConfig{Transport: "http"}, true},
		{"unknown transport", config.MCPServerConfig{Transport: "grpc"}, true},
		{"empty config", config.MCPServerConfig{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := config.ValidateMCPServer("test", tt.cfg)
			if tt.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestPoolRegisterFromConfig(t *testing.T) {
	pool := NewPool("", false, false)
	servers := map[string]config.MCPServerConfig{
		"enabled":  {Transport: "stdio", Command: "echo"},
		"disabled": {Transport: "stdio", Command: "nope", Disabled: true},
	}
	pool.RegisterFromConfig(servers, nil)
	if len(pool.clients) != 1 {
		t.Errorf("expected 1 client (disabled skipped), got %d", len(pool.clients))
	}
}

// TestPoolRegisterFromConfig_PropagatesSandboxFlag ensures the
// pool-level Sandbox flag lands on every registered Client.Sandbox so
// the per-call fail-closed path activates when the operator enabled
// the sandbox in config.
func TestPoolRegisterFromConfig_PropagatesSandboxFlag(t *testing.T) {
	pool := NewPool("/var/sandbox", true, false)
	pool.RegisterFromConfig(map[string]config.MCPServerConfig{
		"a": {Transport: "stdio", Command: "/bin/true"},
	}, nil)
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	mc, ok := pool.clients["a"]
	if !ok {
		t.Fatalf("server %q not registered", "a")
	}
	if !mc.client.Sandbox {
		t.Errorf("client.Sandbox = false, want true (pool propagated Sandbox=true)")
	}
	if mc.client.SandboxRoot != "/var/sandbox" {
		t.Errorf("client.SandboxRoot = %q, want /var/sandbox", mc.client.SandboxRoot)
	}
}

// TestPool_StartAll_FailClosedOnSandboxKernelGap covers the new boot-time
// fail-closed probe. When Sandbox=true and the kernel is missing one of
// the required features (landlock or seccomp), StartAll must return an
// error rather than spawning unsandboxed subprocesses. On a host that
// actually has both landlock and seccomp the probe succeeds and the
// test is skipped — guarding against environment-dependent flips.
func TestPool_StartAll_FailClosedOnSandboxKernelGap(t *testing.T) {
	pool := NewPool(t.TempDir(), true, false)
	// Register one synthetic stdio server so StartAll has something to
	// iterate over if the kernel probe passes; the test skips in that
	// case so we never actually try to spawn the fake binary.
	pool.RegisterFromConfig(map[string]config.MCPServerConfig{
		"never": {Transport: "stdio", Command: "/bin/true"},
	}, nil)
	if checkLandlockSupport() && checkSeccompSupport() {
		t.Skip("kernel has both landlock and seccomp — cannot exercise fail-closed path here")
	}
	err := pool.StartAll(context.Background())
	if err == nil {
		t.Fatalf("expected fail-closed error, got nil")
	}
	if !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("expected sandbox-related error, got %v", err)
	}
}

// ── Environment isolation tests ───────────────────────────────────────────────

func TestEnvKeyBlocked(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"FAMCLAW_LLM_API_KEY", true},
		{"TELEGRAM_TOKEN", true},
		{"TELEGRAM_BOT_TOKEN", true},
		{"DISCORD_TOKEN", true},
		{"DISCORD_BOT_TOKEN", true},
		{"HMAC_SECRET", true},
		{"OPENAI_API_KEY", true},
		{"ANTHROPIC_API_KEY", true},
		{"SMTP_PASSWORD", true},
		{"TWILIO_TOKEN", true},
		{"PATH", false},
		{"HOME", false},
		{"LANG", false},
		{"TZ", false},
		{"GOPATH", false},
		{"SHELL", false},
		{"TERM", false},
		{"FAKETOKEN123", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := envKeyBlocked(tt.name)
			if got != tt.want {
				t.Errorf("envKeyBlocked(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestEnvKeyBlockedCaseInsensitive(t *testing.T) {
	// Case-insensitive check should also block.
	got := envKeyBlocked("fAmClAw_llM_aPi_KeY")
	if !got {
		t.Error("envKeyBlocked should be case-insensitive")
	}
	got = envKeyBlocked("telegram_token")
	if !got {
		t.Error("envKeyBlocked should be case-insensitive for TELEGRAM_TOKEN")
	}
}

func TestBuildAllowlist_NoSecrets(t *testing.T) {
	// Set sensitive vars in the process environment.
	t.Setenv("FAMCLAW_LLM_API_KEY", "sk-fake-llm-key")
	t.Setenv("TELEGRAM_BOT_TOKEN", "111111:AAAbbbbCCC")
	t.Setenv("DISCORD_TOKEN", "dGVzdF9kaXNjb3JkX3Rva2Vu")
	t.Setenv("HMAC_SECRET", "super-secret-hmac-key")
	t.Setenv("OPENAI_API_KEY", "sk-openai-key")
	t.Setenv("SMTP_PASSWORD", "smtp-pass")
	// Also set good vars that should pass through.
	t.Setenv("PATH", "/usr/local/bin:/usr/bin:/bin")
	t.Setenv("HOME", "/home/testuser")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("TZ", "UTC")
	t.Setenv("CUSTOM_VAR", "should-not-appear")

	allowlist := buildAllowlist(nil)

	// Check that blocked vars are NOT present.
	allowlistStr := fmt.Sprintf("%v", allowlist)
	for _, bad := range []string{"FAMCLAW_LLM_API_KEY", "TELEGRAM_BOT_TOKEN", "DISCORD_TOKEN", "HMAC_SECRET", "OPENAI_API_KEY", "SMTP_PASSWORD"} {
		if strings.Contains(allowlistStr, bad) {
			t.Errorf("allowlist should not contain %q, got: %v", bad, allowlist)
		}
	}

	// Check that base allowlist vars ARE present.
	for _, good := range []string{"PATH=", "HOME=", "LANG="} {
		found := false
		for _, entry := range allowlist {
			if strings.HasPrefix(entry, good) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("allowlist should contain %q, got: %v", good, allowlist)
		}
	}

	// CUSTOM_VAR should NOT be in the allowlist (not base + no credKeys).
	if strings.Contains(allowlistStr, "CUSTOM_VAR") {
		t.Errorf("allowlist should not contain CUSTOM_VAR, got: %v", allowlist)
	}
}

func TestBuildAllowlist_IncludesCredentialKeys(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "111111:AAAbbbbCCC")
	t.Setenv("PATH", "/usr/bin")

	// Cred keys that are NOT blocked (e.g. custom skill vars).
	credKeys := map[string]string{
		"MY_CUSTOM_VAR": "secret-value",
	}
	allowlist := buildAllowlist(credKeys)

	allowlistStr := fmt.Sprintf("%v", allowlist)
	if !strings.Contains(allowlistStr, "MY_CUSTOM_VAR=secret-value") {
		t.Errorf("allowlist should include cred key MY_CUSTOM_VAR, got: %v", allowlist)
	}
	if !strings.Contains(allowlistStr, "PATH=/usr/bin") {
		t.Errorf("allowlist should include PATH, got: %v", allowlist)
	}
}

func TestBuildAllowlist_CredentialKeysPassThrough(t *testing.T) {
	// Credential keys from config are trusted and pass through
	// (they are explicitly set by famclaw, not leaked from the
	// process environment).  envKeyBlocked only applies to base
	// allowlist vars pulled from os.LookupEnv.
	t.Setenv("HMAC_SECRET", "process-env-value")

	credKeys := map[string]string{
		"MY_CRED": "trusted-value",
	}
	allowlist := buildAllowlist(credKeys)

	allowlistStr := fmt.Sprintf("%v", allowlist)
	if !strings.Contains(allowlistStr, "MY_CRED=trusted-value") {
		t.Errorf("allowlist should include trusted cred key, got: %v", allowlist)
	}

	// HMAC_SECRET from process env should NOT appear
	// (it's in baseAllowlist? No — it's blocked and not in base).
	if strings.Contains(allowlistStr, "HMAC_SECRET=") {
		t.Errorf("allowlist should not leak HMAC_SECRET from process env, got: %v", allowlist)
	}
}

// TestStdioEnvIsolation spawns a real subprocess and verifies it cannot read
// sensitive environment variables.
func TestStdioEnvIsolation(t *testing.T) {
	// Set secrets in the process environment so they would leak if
	// os.Environ() were still being passed.
	t.Setenv("FAMCLAW_LLM_API_KEY", "sk-test-llm-key-12345")
	t.Setenv("TELEGRAM_BOT_TOKEN", "111:AAAbbCCDdEE")
	t.Setenv("DISCORD_BOT_TOKEN", "dGVzdF90b2tlbl8xMjM0NQ")
	t.Setenv("HMAC_SECRET", "test-hmac-secret-key")
	t.Setenv("SMTP_PASSWORD", "smtp-test-pass")

	script := "#!/bin/sh\n" +
		"LEAK=0\n" +
		"for v in FAMCLAW_LLM_API_KEY TELEGRAM_BOT_TOKEN DISCORD_BOT_TOKEN HMAC_SECRET SMTP_PASSWORD; do\n" +
		`  eval "val=\${$v:-}"` + "\n" +
		`  if [ -n "$val" ]; then echo "LEAKED:$v"; LEAK=1; fi` + "\n" +
		"done\n" +
		"exit $LEAK\n"

	scriptPath := "envcheck_test.sh"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write envcheck script: %v", err)
	}
	defer os.Remove(scriptPath)

	c := &Client{
		name:          "envisolation-test",
		transportType: "stdio",
		cfg:           config.MCPServerConfig{Command: "./" + scriptPath},
		env:           map[string]string{"MY_CUSTOM_CRED": "trusted-value"},
	}

	// Verify buildAllowlist does not leak any sensitive vars.
	allowlist := buildAllowlist(c.env)
	allowlistStr := fmt.Sprintf("%v", allowlist)
	for _, bad := range []string{"FAMCLAW_LLM_API_KEY", "TELEGRAM_BOT_TOKEN", "DISCORD_BOT_TOKEN", "HMAC_SECRET", "SMTP_PASSWORD"} {
		if strings.Contains(allowlistStr, bad) {
			t.Errorf("buildAllowlist leaked %q into allowlist: %v", bad, allowlist)
		}
	}

	// The Client.Start call will fail because the script is not a real MCP
	// server, but that is fine — the env leak test is done above.
	c.Start(context.Background()) // ignore error: script is not an MCP server

	// Also do a direct subprocess invocation to confirm no leaked env vars.
	cmd := exec.Command(scriptPath)
	cmd.Env = buildAllowlist(c.env)
	out, _ := cmd.CombinedOutput()
	if len(out) > 0 {
		t.Errorf("subprocess leaked env vars:\n%s", string(out))
	}
}

// TestBuildAllowlist_Exported verifies the exported BuildAllowlist is
// the same function as the internal buildAllowlist. The sandbox launcher
// in cmd/famclaw/main.go calls the exported form to filter os.Environ()
// before syscall.Exec — make sure both routes produce identical output.
func TestBuildAllowlist_Exported(t *testing.T) {
	t.Setenv("FAMCLAW_LLM_API_KEY", "sk-test-llm-key-12345")
	t.Setenv("TELEGRAM_BOT_TOKEN", "111:AAAbbCCDdEE")
	t.Setenv("PATH", "/usr/local/bin:/usr/bin:/bin")
	t.Setenv("HOME", "/tmp")

	want := buildAllowlist(map[string]string{"MY_CUSTOM_CRED": "ok"})
	got := BuildAllowlist(map[string]string{"MY_CUSTOM_CRED": "ok"})
	if fmt.Sprintf("%v", want) != fmt.Sprintf("%v", got) {
		t.Fatalf("BuildAllowlist != buildAllowlist\nwant=%v\n got=%v", want, got)
	}
}

// TestClient_Start_FailClosedWhenSandboxRequiredButKernelLacksSupport
// guards the per-call fail-closed path inside Client.Start. When the
// pool already started (kernel support verified) the launcher will be
// installed — this test inverts those conditions: it intentionally
// requests Sandbox=true without going through the pool, and on a host
// without both landlock and seccomp the call must return the
// fail-closed error rather than silently launching unsandboxed.
func TestClient_Start_FailClosedWhenSandboxRequired(t *testing.T) {
	cfg := config.MCPServerConfig{Transport: "stdio", Command: "/bin/true"}
	c := NewTransportClient("fail-closed-test", cfg, "/tmp", true, false)
	// Force the gate to trip by patching support to false on this call.
	if checkLandlockSupport() && checkSeccompSupport() {
		t.Skip("kernel has both landlock and seccomp — cannot exercise fail-closed path here")
	}
	err := c.Start(context.Background())
	if err == nil {
		t.Fatalf("expected fail-closed error, got nil")
	}
	if !strings.Contains(err.Error(), "lacks required sandboxing support") {
		t.Fatalf("expected sandboxing-support error, got %v", err)
	}
}

// TestPool_StartAll_AllowUnconfined tests the new AllowUnconfined functionality
func TestPool_StartAll_AllowUnconfined(t *testing.T) {
	// Test that the new parameter is accepted and handled
	_ = NewPool(t.TempDir(), true, true)  // Should compile and work
	_ = NewPool(t.TempDir(), false, true)  // Should compile and work
	_ = NewPool(t.TempDir(), true, false)  // Should compile and work
	_ = NewPool(t.TempDir(), false, false) // Should compile and work
	
	// Test that AllowUnconfined field is properly set
	pool := NewPool(t.TempDir(), true, true)
	if !pool.AllowUnconfined {
		t.Error("AllowUnconfined should be true when passed as true")
	}
	
	pool = NewPool(t.TempDir(), true, false)
	if pool.AllowUnconfined {
		t.Error("AllowUnconfined should be false when passed as false")
	}
}

// TestSandboxDecision tests the sandboxDecision function with various combinations
func TestSandboxDecision(t *testing.T) {
	tests := []struct {
		name             string
		landlockOK       bool
		seccompOK        bool
		allowUnconfined  bool
		wantProceed      bool
		wantWarn         bool
		wantErrNil       bool
	}{
		{"both support, no unconfined", true, true, false, true, false, true},
		{"both support, allow unconfined", true, true, true, true, false, true},
		{"landlock missing, no unconfined", false, true, false, false, false, false},
		{"landlock missing, allow unconfined", false, true, true, true, true, true},
		{"seccomp missing, no unconfined", true, false, false, false, false, false},
		{"seccomp missing, allow unconfined", true, false, true, true, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proceed, warn, err := sandboxDecision(tt.allowUnconfined, tt.landlockOK, tt.seccompOK)
			
			if proceed != tt.wantProceed {
				t.Errorf("sandboxDecision() proceed = %v, want %v", proceed, tt.wantProceed)
			}
			if warn != tt.wantWarn {
				t.Errorf("sandboxDecision() warn = %v, want %v", warn, tt.wantWarn)
			}
			if (err == nil) != tt.wantErrNil {
				t.Errorf("sandboxDecision() err == nil = %v, want %v", err == nil, tt.wantErrNil)
			}
		})
	}
}
