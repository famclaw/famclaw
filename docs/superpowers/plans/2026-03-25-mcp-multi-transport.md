# MCP Multi-Transport Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Support stdio, HTTP, and SSE MCP transports so skills work on constrained devices (Android, RPi) by connecting to remote MCP servers, not just spawning local processes.

**Architecture:** Transport type is declared per-server in config.yaml (not SKILL.md — skills are portable, transport is deployment-specific). The pool creates the right mcp-go client type based on config. All three transports return the same `client.MCPClient` interface, so the agent/pool layer is unchanged.

**Tech Stack:** Go, github.com/mark3labs/mcp-go v0.45.0 (`client`, `client/transport`, `mcp`, `server` packages)

---

## Design Decisions

**Config lives in config.yaml, not SKILL.md.** SKILL.md is shared across runtimes — it describes what a tool does, not where it runs. The same skill (seccheck) runs as stdio on Mac and HTTP on RPi. PicoClaw does the same.

**Disabled by default uses `Disabled` bool.** Go zero-value for bool is `false`, so a missing field means enabled. This matches skillbridge's pattern (enabled by default, explicit `.disabled` marker to turn off).

**Settings API deferred.** MCP server config is a deployment concern, not a parent-facing setting. Ship in a follow-up PR.

**Env var expansion handled by config.Load().** It already calls `os.ExpandEnv()` on the entire YAML. No MCP-layer expansion needed.

---

## Config Schema

```yaml
skills:
  dir: "./skills"
  mcp_servers:
    seccheck:                          # stdio — local process
      transport: stdio
      command: seccheck
      args: ["--json"]

    remote-tools:                      # http — remote server
      transport: http
      url: "http://192.168.1.10:3001/mcp"
      headers:
        Authorization: "Bearer ${MCP_TOKEN}"

    legacy:                            # sse — legacy MCP servers
      transport: sse
      url: "http://192.168.1.10:3002/sse"
      disabled: true                   # explicitly disabled
```

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `internal/config/config.go` | Modify | Add `MCPServerConfig` struct, validation, `Disabled` field |
| `internal/mcp/client.go` | Rewrite | Transport-agnostic client using mcp-go's 3 constructors |
| `internal/mcp/pool.go` | Modify | `RegisterFromConfig()`, fix `StartAll()` map bug, update restart |
| `internal/mcp/mcp_test.go` | Rewrite | Update `newTestClient` for new struct, add transport tests |
| `cmd/famclaw/main.go` | Modify | Load MCP servers from config, register into pool |
| `config.yaml` | Modify | Add commented `mcp_servers` example |
| `docs/SKILLS.md` | Modify | Document MCP transport options |

---

### Task 1: Add MCP server config types with validation

**Files:**
- Modify: `internal/config/config.go:71-77`
- Modify: `config.yaml:77-85`

- [ ] **Step 1: Write the config types**

In `internal/config/config.go`, add after `NtfyConfig`:

```go
// MCPServerConfig defines an MCP tool server connection.
type MCPServerConfig struct {
	Transport string            `yaml:"transport"`           // stdio | http | sse
	Command   string            `yaml:"command,omitempty"`   // stdio only
	Args      []string          `yaml:"args,omitempty"`      // stdio only
	URL       string            `yaml:"url,omitempty"`       // http/sse only
	Headers   map[string]string `yaml:"headers,omitempty"`   // http/sse only
	Disabled  bool              `yaml:"disabled,omitempty"`  // false = enabled (default)
}
```

Extend `SkillsConfig`:

```go
type SkillsConfig struct {
	Dir            string                      `yaml:"dir"`
	AutoSecCheck   bool                        `yaml:"auto_seccheck"`
	BlockOnFail    bool                        `yaml:"block_on_fail"`
	OpenClawCompat bool                        `yaml:"openclaw_compat"`
	MCPServers     map[string]MCPServerConfig  `yaml:"mcp_servers,omitempty"`
}
```

- [ ] **Step 2: Add validation function**

```go
// ValidateMCPServer checks that an MCP server config is well-formed.
func ValidateMCPServer(name string, cfg MCPServerConfig) error {
	transport := cfg.Transport
	if transport == "" {
		if cfg.Command != "" {
			transport = "stdio"
		} else if cfg.URL != "" {
			transport = "http"
		} else {
			return fmt.Errorf("MCP server %q: transport, command, or url required", name)
		}
	}
	switch transport {
	case "stdio":
		if cfg.Command == "" {
			return fmt.Errorf("MCP server %q: stdio transport requires command", name)
		}
	case "http", "sse":
		if cfg.URL == "" {
			return fmt.Errorf("MCP server %q: %s transport requires url", name, transport)
		}
	default:
		return fmt.Errorf("MCP server %q: unknown transport %q (use stdio, http, or sse)", name, transport)
	}
	return nil
}
```

- [ ] **Step 3: Add commented example to config.yaml**

After the `openclaw_compat` line in the skills section:

```yaml
  # MCP tool servers — tools discovered automatically via tools/list
  # mcp_servers:
  #   local-tools:
  #     transport: stdio
  #     command: seccheck
  #     args: ["--json"]
  #   remote-tools:
  #     transport: http
  #     url: "http://192.168.1.10:3001/mcp"
  #     headers:
  #       Authorization: "Bearer ${MCP_TOKEN}"
```

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: compiles cleanly

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go config.yaml
git commit -m "feat(config): add MCPServerConfig with transport types and validation"
```

---

### Task 2: Refactor MCP client for multi-transport

**Files:**
- Rewrite: `internal/mcp/client.go`
- Modify: `internal/mcp/mcp_test.go` (update `newTestClient` to compile with new struct)

- [ ] **Step 1: Write failing tests**

Add to `internal/mcp/mcp_test.go` (replace existing `newTestClient`):

```go
func TestNewTransportClient_Stdio(t *testing.T) {
	cfg := config.MCPServerConfig{Transport: "stdio", Command: "nonexistent"}
	c := NewTransportClient("test", cfg)
	if c.transportType != "stdio" {
		t.Errorf("transport = %q, want stdio", c.transportType)
	}
}

func TestNewTransportClient_HTTP(t *testing.T) {
	cfg := config.MCPServerConfig{Transport: "http", URL: "http://localhost:9999/mcp"}
	c := NewTransportClient("test", cfg)
	if c.transportType != "http" {
		t.Errorf("transport = %q, want http", c.transportType)
	}
}

func TestNewTransportClient_SSE(t *testing.T) {
	cfg := config.MCPServerConfig{Transport: "sse", URL: "http://localhost:9999/sse"}
	c := NewTransportClient("test", cfg)
	if c.transportType != "sse" {
		t.Errorf("transport = %q, want sse", c.transportType)
	}
}

func TestNewTransportClient_DefaultStdio(t *testing.T) {
	cfg := config.MCPServerConfig{Command: "some-cmd"}
	c := NewTransportClient("test", cfg)
	if c.transportType != "stdio" {
		t.Errorf("empty transport with command should default to stdio")
	}
}

func TestNewTransportClient_DefaultHTTP(t *testing.T) {
	cfg := config.MCPServerConfig{URL: "http://example.com/mcp"}
	c := NewTransportClient("test", cfg)
	if c.transportType != "http" {
		t.Errorf("empty transport with url should default to http")
	}
}
```

Also update `newTestClient` helper to use new struct:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mcp/... -run TestNewTransportClient -v`
Expected: FAIL — `NewTransportClient` undefined

- [ ] **Step 3: Rewrite client.go**

```go
package mcp

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/famclaw/famclaw/internal/config"
)

// Client wraps an mcp-go client for any transport (stdio, HTTP, SSE).
type Client struct {
	name          string
	transportType string // stdio | http | sse | inprocess (test)
	cfg           config.MCPServerConfig
	inner         client.MCPClient
	tools         []mcp.Tool
	closed        bool
}

// NewTransportClient creates an MCP client from config.
// Transport auto-detected from fields if not set explicitly.
func NewTransportClient(name string, cfg config.MCPServerConfig) *Client {
	t := cfg.Transport
	if t == "" {
		if cfg.Command != "" {
			t = "stdio"
		} else if cfg.URL != "" {
			t = "http"
		}
	}
	return &Client{name: name, transportType: t, cfg: cfg}
}

// Start connects to the MCP server and performs the initialize handshake.
func (c *Client) Start(ctx context.Context) error {
	if c.inner != nil {
		return nil
	}

	// Timeout for remote transports
	startCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var inner client.MCPClient
	var err error

	switch c.transportType {
	case "stdio":
		inner, err = client.NewStdioMCPClient(c.cfg.Command, nil, c.cfg.Args...)
	case "http":
		var opts []transport.StreamableHTTPCOption
		if len(c.cfg.Headers) > 0 {
			opts = append(opts, transport.WithHTTPHeaders(c.cfg.Headers))
		}
		inner, err = client.NewStreamableHTTPMCPClient(c.cfg.URL, opts...)
	case "sse":
		var opts []transport.ClientOption
		if len(c.cfg.Headers) > 0 {
			opts = append(opts, transport.WithHeaders(c.cfg.Headers))
		}
		inner, err = client.NewSSEMCPClient(c.cfg.URL, opts...)
	default:
		return fmt.Errorf("unknown MCP transport %q for server %q", c.transportType, c.name)
	}
	if err != nil {
		return fmt.Errorf("creating MCP client %q (%s): %w", c.name, c.transportType, err)
	}
	c.inner = inner

	// Initialize handshake
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "famclaw", Version: "1.0.0"}

	_, err = c.inner.Initialize(startCtx, initReq)
	if err != nil {
		c.inner.Close()
		c.inner = nil
		return fmt.Errorf("MCP initialize %q: %w", c.name, err)
	}

	// List available tools
	toolsResult, err := c.inner.ListTools(startCtx, mcp.ListToolsRequest{})
	if err != nil {
		log.Printf("[mcp] tools/list failed for %q: %v", c.name, err)
	} else {
		c.tools = toolsResult.Tools
	}

	return nil
}

// CallTool invokes a tool on the MCP server.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	if c.inner == nil {
		if err := c.Start(ctx); err != nil {
			return nil, err
		}
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	result, err := c.inner.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("calling tool %q on %q: %w", name, c.name, err)
	}
	return result, nil
}

// Tools returns the list of available tools.
func (c *Client) Tools() []mcp.Tool { return c.tools }

// Stop terminates the MCP server connection.
func (c *Client) Stop() {
	if c.inner != nil && !c.closed {
		c.inner.Close()
		c.closed = true
		c.inner = nil
	}
}
```

**Note on constructors:** `NewStdioMCPClient` auto-starts the process. `NewStreamableHTTPMCPClient` and `NewSSEMCPClient` create the client but connect on `Initialize()`. All three return `(MCPClient, error)`.

**Note:** Check the actual mcp-go export name. It may be `client.NewStreamableHttpClient` not `NewStreamableHTTPMCPClient`. Verify by checking the import and adjusting. The mcp-go docs show both `client.NewStreamableHttpClient()` and `transport.NewStreamableHTTP()` — use whichever compiles.

- [ ] **Step 4: Run all tests**

Run: `go test ./internal/mcp/... -v`
Expected: all pass (transport constructor tests + existing InProcess tests)

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/client.go internal/mcp/mcp_test.go
git commit -m "feat(mcp): multi-transport client — stdio, HTTP, SSE via mcp-go"
```

---

### Task 3: Update pool for config-driven registration

**Files:**
- Modify: `internal/mcp/pool.go`

- [ ] **Step 1: Write failing test**

```go
func TestPoolRegisterFromConfig(t *testing.T) {
	pool := NewPool()
	servers := map[string]config.MCPServerConfig{
		"enabled":  {Transport: "stdio", Command: "echo"},
		"disabled": {Transport: "stdio", Command: "nope", Disabled: true},
	}
	pool.RegisterFromConfig(servers)
	if len(pool.clients) != 1 {
		t.Errorf("expected 1 client (disabled skipped), got %d", len(pool.clients))
	}
}
```

- [ ] **Step 2: Run test — should fail**

Run: `go test ./internal/mcp/... -run TestPoolRegisterFromConfig -v`
Expected: FAIL

- [ ] **Step 3: Implement RegisterFromConfig and fix pool**

Update `managedClient`:

```go
type managedClient struct {
	client     *Client
	name       string
	cfg        config.MCPServerConfig
	restartCnt int
}
```

Add `RegisterFromConfig`:

```go
func (p *Pool) RegisterFromConfig(servers map[string]config.MCPServerConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for name, cfg := range servers {
		if cfg.Disabled {
			continue
		}
		if err := config.ValidateMCPServer(name, cfg); err != nil {
			log.Printf("[mcp-pool] skip %s: %v", name, err)
			continue
		}
		p.clients[name] = &managedClient{
			client: NewTransportClient(name, cfg),
			name:   name,
			cfg:    cfg,
		}
	}
}
```

Fix `StartAll` — add tool aliases without replacing the map:

```go
func (p *Pool) StartAll(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	toolAliases := make(map[string]*managedClient)
	for name, mc := range p.clients {
		if err := mc.client.Start(ctx); err != nil {
			log.Printf("[mcp-pool] failed to start %s: %v", name, err)
			continue // keep entry for later retry
		}
		for _, tool := range mc.client.Tools() {
			toolAliases[tool.Name] = mc
		}
	}
	// Add tool-name aliases (server-name entries preserved)
	for toolName, mc := range toolAliases {
		p.clients[toolName] = mc
	}
	return nil
}
```

Fix `CallTool` restart to use config:

```go
// In the restart block:
mc.client = NewTransportClient(mc.name, mc.cfg)
```

Reset `restartCnt` on successful calls (remote transports have transient failures):

```go
result, err := mc.client.CallTool(ctx, name, args)
if err == nil {
	mc.restartCnt = 0 // reset on success
	return result, nil
}
```

Remove old `Register(cmd, args)` method — nothing calls it.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/mcp/... -v`
Expected: all pass

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/pool.go
git commit -m "feat(mcp): pool RegisterFromConfig with validation, fix StartAll map bug"
```

---

### Task 4: Wire config into main.go

**Files:**
- Modify: `cmd/famclaw/main.go:102-112`

- [ ] **Step 1: Replace pool setup**

Change from:

```go
mcpPool := mcp.NewPool()
reg := skillbridge.NewRegistry(cfg.Skills.Dir)
if skills, err := reg.List(); err == nil {
    for _, sk := range skills {
        if reg.IsEnabled(sk.Name) {
            log.Printf("Skill: %s v%s", sk.Name, sk.Version)
        }
    }
}
```

To:

```go
mcpPool := mcp.NewPool()
if len(cfg.Skills.MCPServers) > 0 {
    mcpPool.RegisterFromConfig(cfg.Skills.MCPServers)
    if err := mcpPool.StartAll(context.Background()); err != nil {
        log.Printf("MCP pool: %v", err)
    }
    tools := mcpPool.ListTools()
    log.Printf("MCP: %d servers configured, %d tools available", len(cfg.Skills.MCPServers), len(tools))
}

// Skills still loaded for prompt injection (independent of MCP)
reg := skillbridge.NewRegistry(cfg.Skills.Dir)
if skills, err := reg.List(); err == nil {
    for _, sk := range skills {
        if reg.IsEnabled(sk.Name) {
            log.Printf("Skill: %s v%s", sk.Name, sk.Version)
        }
    }
}
```

- [ ] **Step 2: Verify build and tests**

Run: `go build ./cmd/famclaw/... && go test ./... -count=1 -timeout 120s`
Expected: builds, all tests pass

- [ ] **Step 3: Commit**

```bash
git add cmd/famclaw/main.go
git commit -m "feat: wire MCP multi-transport pool from config"
```

---

### Task 5: Update docs

**Files:**
- Modify: `docs/SKILLS.md`
- Modify: `README.md`

- [ ] **Step 1: Add MCP transport section to SKILLS.md**

Add after the "MCP integration" section:

```markdown
## MCP Server Transports

FamClaw supports three MCP transport types, configured in `config.yaml`:

### Stdio (local process)
For devices that can run tool binaries locally (Mac, beefy RPi):

\```yaml
mcp_servers:
  seccheck:
    transport: stdio
    command: seccheck
    args: ["--json"]
\```

### HTTP (remote server)
For constrained devices (Android, RPi-as-gateway) connecting to tools on LAN:

\```yaml
mcp_servers:
  remote-tools:
    transport: http
    url: "http://192.168.1.10:3001/mcp"
    headers:
      Authorization: "Bearer ${MCP_TOKEN}"
\```

### SSE (legacy)
For older MCP servers using Server-Sent Events:

\```yaml
mcp_servers:
  legacy:
    transport: sse
    url: "http://192.168.1.10:3002/sse"
\```

Servers are enabled by default. Add `disabled: true` to skip a server.
```

- [ ] **Step 2: Update README MCP line**

Change: `| \`internal/mcp\` | Built | 8 pass — ...`
To reflect multi-transport support.

- [ ] **Step 3: Commit**

```bash
git add docs/SKILLS.md README.md
git commit -m "docs: MCP multi-transport configuration guide"
```

---

## Verification

After all tasks:

```bash
go build ./...
go test ./... -v -count=1
go test -tags integration ./... -v
make cross
go vet ./...
```

---

## PR Strategy

**Single PR:** `feat/mcp-multi-transport` — all 5 tasks are tightly coupled.

**Not in this PR (deferred):**
- Settings API for MCP servers (deployment concern, not parent-facing)
- Integration test with real HTTP MCP server (follow-up)
- Android auto-detection of transport mode (future)
