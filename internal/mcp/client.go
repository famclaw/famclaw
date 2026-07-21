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
	"strings"
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
	env     map[string]string // per-skill credential env vars
	inner   client.MCPClient
	tools   []mcp.Tool
	closed  bool
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
	if c.inner != nil {
		return nil
	}

	// Timeout for connection — remote servers may be slow or unreachable
	startCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var inner client.MCPClient
	var err error

	switch c.transportType {
	case "stdio":
		// Build a minimal environment: base allowlist + skill-declared vars +
		// per-skill credentials.  Never passes os.Environ() — a skill inherits
		// only the variables explicitly named in its allowlist (or the base set
		// if the skill declares none), plus its own injected credentials.
		allowlist := BuildAllowlist(c.env)
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

// baseAllowlist is the minimal set of environment variables every subprocess
// receives.  No secrets live in this list.
var baseAllowlist = []string{"HOME", "LANG", "PATH", "TZ"}

// blockedKeys are environment variable names that are never forwarded to a
// skill subprocess, regardless of any allowlist.  They guard against future
// allowlist mistakes and cover every credential famclaw holds.
var blockedKeys = map[string]struct{}{
	"FAMCLAW_LLM_API_KEY":   {},
	"FAMCLAW_HMAC_SECRET":   {},
	"FAMCLAW_PARENT_PIN":    {},
	"FAMCLAW_SMTP_PASSWORD": {},
	"FAMCLAW_TWILIO_TOKEN":  {},
	"FAMCLAW_VAULT_SALT":    {},
	"DISCORD_TOKEN":         {},
	"TELEGRAM_TOKEN":        {},
	"WHATSAPP_TOKEN":        {},
	"HMAC_SECRET":           {},
	"LLM_API_KEY":           {},
	"OPENAI_API_KEY":        {},
	"ANTHROPIC_API_KEY":     {},
	"SENDGRID_API_KEY":      {},
	"SMTP_PASSWORD":         {},
	"TWILIO_TOKEN":          {},
	"VAULT_SALT":            {},
}

// envKeyBlocked reports whether name is a blocked environment variable.
// Matching is case-insensitive to block both FAMCLAW_LLM_API_KEY and
// variations like FAmClAw_LlM_aPi_KeY.
func envKeyBlocked(name string) bool {
	if _, ok := blockedKeys[name]; ok {
		return true
	}
	upper := strings.ToUpper(name)
	if _, ok := blockedKeys[upper]; ok {
		return true
	}
	// Block anything ending with _BOT_TOKEN (Telegram/Discord/WhatsApp/etc.)
	if strings.HasSuffix(upper, "_BOT_TOKEN") || strings.HasSuffix(upper, "_TOKEN") {
		return true
	}
	return false
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
// Strict secret-blocklist: see blockedKeys / envKeyBlocked. Skills never
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
		if v != "" {
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
