// Package mcp provides MCP client management for FamClaw skill tool servers.
// Supports stdio, HTTP (StreamableHTTP), and SSE transports via mcp-go.
package mcp

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"sync"

	"time"

	"github.com/elastic/go-seccomp-bpf"
	syscall "github.com/landlock-lsm/go-landlock/landlock/syscall"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/famclaw/famclaw/internal/config"
)

// Client wraps an mcp-go client for any transport (stdio, HTTP, SSE).
type Client struct {
	name          string
	transportType string // stdio | http | sse | inprocess (test only)
	cfg           config.MCPServerConfig
	SandboxRoot   string // sandbox root for file operations
	// Sandbox == true means this stdio client MUST launch through the
	// landlock+seccomp sandbox launcher (and Pool.StartAll has already
	// verified kernel support). When false, the launcher is skipped —
	// either because the operator set Tools.Sandbox.Enabled=false
	// (explicit opt-out) or because the per-server config did not pick
	// the stdio sandbox path.
	Sandbox bool
	// AllowUnconfined == true means this client will allow running without
	// OS sandboxing when kernel support is missing. This is an explicit opt-in
	// for platforms like macOS that lack landlock/seccomp, with a loud warning.
	AllowUnconfined bool
	env             map[string]string // per-skill credential env vars
	mu              sync.Mutex
	startGate       chan struct{}
	inner           client.MCPClient
	lifetime        context.Context
	cancelLifetime  context.CancelFunc
	tools           []mcp.Tool
	closed          bool
	ready           bool
	generation      uint64
}

// checkLandlockSupport returns true if Landlock is supported on the current system
func checkLandlockSupport() bool {
	// Try to get the Landlock ABI version - returns error if not supported
	_, err := syscall.LandlockGetABIVersion()
	return err == nil
}

// checkSeccompSupport returns true if seccomp is supported on the current system
func checkSeccompSupport() bool {
	return seccomp.Supported()
}

// NewTransportClient creates an MCP client from config.
// Transport is auto-detected from fields if not set explicitly:
//   - command present → stdio
//   - url present → http
//
// sandboxEnabled mirrors the pool-level Sandbox flag (Tools.Sandbox.Enabled
// in config). It is recorded on the client so Start() can refuse to launch
// when kernel support is missing, instead of silently downgrading to an
// unsandboxed process.
func NewTransportClient(name string, cfg config.MCPServerConfig, sandboxRoot string, sandboxEnabled bool, allowUnconfined bool) *Client {
	t := cfg.Transport
	if t == "" {
		if cfg.Command != "" {
			t = "stdio"
		} else if cfg.URL != "" {
			t = "http"
		}
	}
	return &Client{
		name:            name,
		transportType:   t,
		cfg:             cfg,
		SandboxRoot:     sandboxRoot,
		Sandbox:         sandboxEnabled,
		AllowUnconfined: allowUnconfined,
	}
}

// Start connects to the MCP server and performs the initialize handshake.
// For stdio: spawns the process. For HTTP/SSE: connects to the remote server.
func (c *Client) Start(ctx context.Context) error {
	return c.start(ctx, true)
}

// start serializes handshakes while allowing Stop to detach concurrently.
func (c *Client) start(ctx context.Context, reopen bool) error {
	c.mu.Lock()
	if c.inner != nil && c.ready {
		err := ctx.Err()
		c.mu.Unlock()
		return err
	}
	c.mu.Unlock()

	startCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c.mu.Lock()
	generation := c.generation
	if c.startGate == nil {
		c.startGate = make(chan struct{}, 1)
		c.startGate <- struct{}{}
	}
	gate := c.startGate
	c.mu.Unlock()
	select {
	case <-startCtx.Done():
		return startCtx.Err()
	case <-gate:
	}
	defer func() { gate <- struct{}{} }()
	c.mu.Lock()
	if err := startCtx.Err(); err != nil {
		c.mu.Unlock()
		return err
	}
	if c.generation != generation {
		c.mu.Unlock()
		return errClientStopped
	}
	if c.inner != nil && c.ready {
		c.mu.Unlock()
		return nil
	}
	if c.closed && !reopen {
		c.mu.Unlock()
		return errClientStopped
	}
	c.closed = false
	c.mu.Unlock()

	var inner client.MCPClient
	var err error

	switch c.transportType {
	case "stdio":
		// Build a minimal environment: base allowlist + skill-declared vars +
		// per-skill credentials.  Never passes os.Environ() — a skill inherits
		// only the variables explicitly named in its allowlist (or the base set
		// if the skill declares none), plus its own injected credentials.
		credentials := make(map[string]string, len(c.env)+len(c.cfg.Env))
		for key, value := range c.env {
			credentials[key] = value
		}
		for key, value := range c.cfg.Env {
			credentials[key] = value
		}
		allowlist := BuildAllowlist(credentials)
		var opts []transport.StdioOption
		// Sandbox decision: when the deployment enabled Tools.Sandbox the
		// stdio server must launch through the landlock+seccomp launcher.
		// Pool.StartAll already verified kernel support at boot and
		// refused to start if either was missing; this per-call check is
		// defence-in-depth so a misconfigured pool cannot silently fall
		// back to an unsandboxed subprocess.
		if c.Sandbox {
			if c.SandboxRoot == "" {
				return fmt.Errorf("sandbox root not set for sandboxed MCP server %q", c.name)
			}
			landlockOK := checkLandlockSupport()
			seccompOK := checkSeccompSupport()
			if !landlockOK || !seccompOK {
				if !c.AllowUnconfined {
					return fmt.Errorf("kernel lacks required sandboxing support (landlock=%v, seccomp=%v); refusing to launch MCP server %q unsandboxed (set tools.sandbox.allow_unconfined=true to allow as explicit opt-in)",
						landlockOK, seccompOK, c.name)
				}
				log.Printf("⚠️  kernel lacks required sandboxing support (landlock=%v, seccomp=%v) but tools.sandbox.allow_unconfined=true — launching MCP server %q without OS sandboxing (EXPLICIT OPT-IN, NOT RECOMMENDED FOR PRODUCTION)",
					landlockOK, seccompOK, c.name)
				// Skip adding sandbox launcher - fall through to default mcp-go exec
			} else {
				opts = append(opts, transport.WithCommandFunc(func(ctx context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
					launcherArgs := []string{
						"-sandbox-launcher",
						"--sandbox-root", c.SandboxRoot,
						"--",
						command,
					}
					launcherArgs = append(launcherArgs, args...)

					cmd := exec.CommandContext(ctx, os.Args[0], launcherArgs...)
					// Build environment: base allowlist + skill-declared vars + per-skill credentials
					cmd.Env = env
					return cmd, nil
				}))
			}
		}
		// Tools.Sandbox.Enabled=false branch: the sandbox launcher is
		// skipped and the subprocess is started without landlock/seccomp
		// (operator explicitly opted out). We do NOT add WithCommandFunc,
		// so mcp-go falls back to its default exec path.
		inner, err = client.NewStdioMCPClientWithOptions(c.cfg.Command, allowlist, c.cfg.Args, opts...)
	case "http":
		var opts []transport.StreamableHTTPCOption
		if len(c.cfg.Headers) > 0 {
			opts = append(opts, transport.WithHTTPHeaders(c.cfg.Headers))
		}
		inner, err = client.NewStreamableHttpClient(c.cfg.URL, opts...)
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
	c.mu.Lock()
	if c.closed || c.generation != generation {
		c.mu.Unlock()
		inner.Close()
		return errClientStopped
	}
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	c.inner = inner
	c.lifetime = lifetime
	c.cancelLifetime = cancelLifetime
	c.mu.Unlock()

	// Keep ownership until startup is ready. On an unexpected unwind, close
	// only this still-attached transport before releasing the startup gate.
	ready := false
	defer func() {
		if !ready {
			c.closeUnready(inner)
		}
	}()

	// SSE keeps its stream on the Start context after initialization. Bind it
	// to this transport's lifetime while allowing startup cancellation.
	stopStartupCancel := context.AfterFunc(startCtx, cancelLifetime)
	defer stopStartupCancel()
	if c.transportType == "sse" {
		starter, ok := inner.(interface{ Start(context.Context) error })
		if !ok {
			return fmt.Errorf("MCP transport %q does not support startup", c.name)
		}
		if err := starter.Start(lifetime); err != nil {
			if ctxErr := startCtx.Err(); ctxErr != nil {
				return fmt.Errorf("starting MCP transport %q: %w", c.name, ctxErr)
			}
			return fmt.Errorf("starting MCP transport %q: %w", c.name, err)
		}
	}

	// Initialize handshake
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "famclaw", Version: "1.0.0"}

	_, err = inner.Initialize(startCtx, initReq)
	if err != nil {
		return fmt.Errorf("MCP initialize %q: %w", c.name, err)
	}

	// List available tools
	toolsResult, err := inner.ListTools(startCtx, mcp.ListToolsRequest{})
	stopStartupCancel()
	if err := startCtx.Err(); err != nil {
		return err
	}
	if err := c.completeStartup(inner, generation, toolsResult, err); err != nil {
		return err
	}
	ready = true
	return nil
}

// completeStartup releases c.mu before the caller's startup cleanup can run.
func (c *Client) completeStartup(inner client.MCPClient, generation uint64, toolsResult *mcp.ListToolsResult, listErr error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inner != inner || c.closed || c.generation != generation {
		return errClientStopped
	}
	if listErr != nil {
		log.Printf("[mcp] tools/list failed for %q: %v", c.name, listErr)
	} else {
		c.tools = toolsResult.Tools
	}
	c.ready = true
	return nil
}

var errClientStopped = fmt.Errorf("MCP client stopped")

// started reports whether a transport is currently attached.
func (c *Client) started() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inner != nil && c.ready
}

// CallTool invokes a tool on the MCP server using a stable transport snapshot.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	if err := c.start(ctx, true); err != nil {
		return nil, err
	}
	c.mu.Lock()
	inner := c.inner
	lifetime := c.lifetime
	c.mu.Unlock()
	if inner == nil {
		return nil, errClientStopped
	}
	callCtx := ctx
	if lifetime != nil {
		if err := lifetime.Err(); err != nil {
			return nil, errClientStopped
		}
		var cancel context.CancelFunc
		callCtx, cancel = context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(lifetime, cancel)
		defer stop()
		if lifetime.Err() != nil {
			return nil, errClientStopped
		}
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	result, err := inner.CallTool(callCtx, req)
	if err != nil {
		return nil, fmt.Errorf("calling tool %q on %q: %w", name, c.name, err)
	}
	return result, nil
}

// Tools returns a snapshot of the available tools.
func (c *Client) Tools() []mcp.Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]mcp.Tool(nil), c.tools...)
}

// closeUnready closes an interrupted startup only while it still owns it.
func (c *Client) closeUnready(inner client.MCPClient) {
	c.mu.Lock()
	owned := c.inner == inner
	if owned {
		c.inner = nil
		c.ready = false
		if c.cancelLifetime != nil {
			c.cancelLifetime()
		}
		c.lifetime = nil
		c.cancelLifetime = nil
	}
	c.mu.Unlock()
	if owned {
		_ = inner.Close()
	}
}

// detach atomically prevents further use of this transport; Close may block.
func (c *Client) detach() client.MCPClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	inner := c.inner
	if c.cancelLifetime != nil {
		c.cancelLifetime()
	}
	c.inner = nil
	c.lifetime = nil
	c.cancelLifetime = nil
	c.ready = false
	c.closed = true
	c.generation++
	return inner
}

// Stop terminates the currently attached transport.
func (c *Client) Stop() {
	if inner := c.detach(); inner != nil {
		inner.Close()
	}
}

// baseAllowlist is the minimal set of environment variables every subprocess
// receives.  No secrets live in this list.
var baseAllowlist = []string{"HOME", "LANG", "PATH", "TZ"}

// envKeyBlocked reports whether name is a blocked environment variable.
// It delegates to config so config-time validation and runtime filtering share
// exactly the same security policy.
func envKeyBlocked(name string) bool {
	return config.IsBlockedMCPEnvKey(name)
}

// BuildAllowlist returns an env []string for os/exec that contains only the
// permitted variables from the current process environment, plus any
// per-skill credential values from credKeys.  The result is sorted for
// deterministic ordering (helps tests).
//
// Exported so the sandbox launcher (cmd/famclaw/main.go) can produce the
// same minimal environment it forwards to syscall.Exec — without the
// launcher having to import os/env-parsing logic of its own.
//
// Strict secret-blocklist: see envKeyBlocked. Skills never
// receive FAMCLAW_* tokens, *_BOT_TOKEN, *_TOKEN, LLM_API_KEY, etc.
func BuildAllowlist(credKeys map[string]string) []string {
	var result []string
	for _, n := range baseAllowlist {
		if v, ok := os.LookupEnv(n); ok && !envKeyBlocked(n) {
			result = append(result, n+"="+v)
		}
	}
	// Credential keys come after base vars so they can override.
	var credNames []string
	for k := range credKeys {
		credNames = append(credNames, k)
	}
	sort.Strings(credNames)
	for _, n := range credNames {
		v := credKeys[n]
		if v != "" && !envKeyBlocked(n) {
			result = append(result, n+"="+v)
		}
	}
	return result
}

// buildAllowlist is the unexported form kept for backward-compatible
// callers within the package.
func buildAllowlist(credKeys map[string]string) []string {
	return BuildAllowlist(credKeys)
}
