package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/famclaw/famclaw/internal/config"
)

// MaxToolCallIterations is the hard limit for tool-call loops in the agent
// pipeline. Bounds the number of times the LLM can request a tool followed
// by another tool in a single turn — prevents runaway behavior when an MCP
// server keeps returning calls indefinitely.
const MaxToolCallIterations = 10

// Pool manages multiple MCP clients (one per server), with lazy start and auto-restart.
type Pool struct {
	clients     map[string]*managedClient // server name + tool name → client
	mu          sync.RWMutex
	reloadMu    sync.Mutex
	SandboxRoot string
	// Sandbox == true means every stdio MCP server is **required** to be
	// launched inside the sandbox-restricted child process (landlock +
	// seccomp). When true, the pool also performs a one-shot probe of
	// kernel support at StartAll time; if the running kernel lacks
	// landlock or seccomp the pool refuses to start any MCP server
	// (fail-closed). Operators who explicitly want to run unconfined
	// MCP subprocesses must set Tools.Sandbox.Enabled=false in config
	// — the default is true (secure-by-default).
	Sandbox bool
	// AllowUnconfined == true means the pool will allow running MCP
	// servers without OS sandboxing when kernel support is missing.
	// This is an explicit opt-in for platforms like macOS that lack
	// landlock/seccomp, with a loud warning at startup.
	AllowUnconfined bool
}

// UpdateFromConfig synchronizes the pool's MCP servers with the provided configuration.
// It adds new servers, updates existing ones that have changed, and removes servers
// that are no longer present in the configuration. It returns names that need a start.
func (p *Pool) UpdateFromConfig(servers map[string]config.MCPServerConfig, credentials map[string]map[string]string) []string {
	p.mu.Lock()
	changed := make([]string, 0)
	// Remove old tool aliases before mutating server clients; otherwise aliases
	// can continue pointing at stopped clients after a configuration change.
	for key, mc := range p.clients {
		if mc != nil && mc.name != key {
			delete(p.clients, key)
		}
	}

	// First, update or add servers that are in the new config
	for name, newCfg := range servers {
		if newCfg.Disabled {
			if mc, exists := p.clients[name]; exists && mc.name == name {
				mc.mu.Lock()
				mc.retire()
				mc.mu.Unlock()
				delete(p.clients, name)
			}
			continue
		}
		if err := config.ValidateMCPServer(name, newCfg); err != nil {
			log.Printf("[mcp-pool] skip %s: %v", name, err)
			continue
		}

		if mc, exists := p.clients[name]; exists {
			// Check if the config has changed
			if mc.cfg.Transport != newCfg.Transport ||
				mc.cfg.Command != newCfg.Command ||
				!sliceEqual(mc.cfg.Args, newCfg.Args) ||
				mc.cfg.URL != newCfg.URL ||
				!mapEqual(mc.cfg.Headers, newCfg.Headers) ||
				!mapEqual(mc.env, credentials[name]) {

				// Stop the existing client without racing a tool call or start.
				mc.mu.Lock()
				mc.retire()
				mc.mu.Unlock()

				// Create a new client with the updated config
				c := NewTransportClient(name, newCfg, p.SandboxRoot, p.Sandbox, p.AllowUnconfined)
				if creds, ok := credentials[name]; ok {
					c.env = creds
				}
				p.clients[name] = &managedClient{
					client: c,
					name:   name,
					cfg:    newCfg,
					env:    c.env,
				}
				changed = append(changed, name)
			}
			// If config hasn't changed, we keep the existing client
		} else {
			// New server - add it
			c := NewTransportClient(name, newCfg, p.SandboxRoot, p.Sandbox, p.AllowUnconfined)
			if creds, ok := credentials[name]; ok {
				c.env = creds
			}
			p.clients[name] = &managedClient{
				client: c,
				name:   name,
				cfg:    newCfg,
				env:    c.env,
			}
			changed = append(changed, name)
		}
	}

	// Remove servers that are no longer in the config
	for name, mc := range p.clients {
		// Skip tool-name aliases (we identify them by mc.name != name)
		if mc.name != name {
			continue
		}
		cfg, exists := servers[name]
		if !exists || cfg.Disabled {
			// Stop and remove the server
			if mc != nil {
				mc.mu.Lock()
				mc.retire()
				mc.mu.Unlock()
				delete(p.clients, name)
			}
		}
	}

	// Rebuild tool-name aliases
	toolAliases := make(map[string]*managedClient)
	for name, mc := range p.clients {
		// Skip tool-name aliases (we identify them by mc.name != name)
		if mc.name != name {
			continue
		}
		for _, tool := range mc.snapshotClient().Tools() {
			toolAliases[tool.Name] = mc
		}
	}
	// Add tool-name aliases, but never overwrite a server entry
	for toolName, mc := range toolAliases {
		// Check if toolName exists as a server entry
		if existingMc, exists := p.clients[toolName]; exists && existingMc.name == toolName {
			log.Printf("[mcp-pool] tool %q shadows server name — skipping alias", toolName)
			continue
		}
		p.clients[toolName] = mc
	}
	p.mu.Unlock()
	return changed
}

// ReloadConfig synchronizes server registrations and starts changed servers outside the pool lock.
func (p *Pool) ReloadConfig(cfg *config.Config) map[string]error {
	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()
	changed := p.UpdateFromConfig(cfg.Skills.MCPServers, cfg.Skills.Credentials)
	results := make(map[string]error, len(changed))
	var wg sync.WaitGroup
	var resultMu sync.Mutex
	for _, name := range changed {
		p.mu.RLock()
		mc := p.clients[name]
		if mc == nil {
			p.mu.RUnlock()
			continue
		}
		mc.stateMu.Lock()
		generation := mc.generation
		mc.stateMu.Unlock()
		p.mu.RUnlock()
		wg.Add(1)
		go func(name string, mc *managedClient, generation uint64) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			mc.mu.Lock()
			mc.stateMu.Lock()
			c := mc.client
			valid := mc.generation == generation
			mc.stateMu.Unlock()
			err := errClientStopped
			if valid {
				err = c.start(ctx, false)
			}
			mc.stateMu.Lock()
			mc.lastError = err
			mc.stateMu.Unlock()
			mc.mu.Unlock()
			resultMu.Lock()
			results[name] = err
			resultMu.Unlock()
		}(name, mc, generation)
	}
	wg.Wait()
	p.refreshToolAliases()
	return results
}

// refreshToolAliases rebuilds tool-name lookups after clients have started.
func (p *Pool) refreshToolAliases() {
	p.mu.Lock()
	defer p.mu.Unlock()
	aliases := make(map[string]*managedClient)
	for name, mc := range p.clients {
		if mc == nil || mc.name != name {
			continue
		}
		tools := mc.snapshotClient().Tools()
		for _, tool := range tools {
			aliases[tool.Name] = mc
		}
	}
	for name, mc := range aliases {
		if existing, ok := p.clients[name]; ok && existing.name == name {
			continue
		}
		p.clients[name] = mc
	}
}

// ServerState describes the connection state for a configured server.
type ServerState struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Status    string `json:"status"`
	Tools     int    `json:"tools"`
	Error     string `json:"error,omitempty"`
}

// ServerStates returns sorted lifecycle snapshots without exposing credentials.
func (p *Pool) ServerStates() []ServerState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	states := make([]ServerState, 0, len(p.clients))
	for name, mc := range p.clients {
		if mc == nil || mc.name != name {
			continue
		}
		mc.mu.Lock()
		state := ServerState{Name: name, Transport: mc.snapshotClient().transportType, Status: "pending", Tools: len(mc.snapshotClient().Tools())}
		if mc.snapshotClient().started() {
			state.Status = "started"
		} else {
			mc.stateMu.Lock()
			if mc.lastError != nil {
				state.Status = "failed"
				state.Error = mc.lastError.Error()
			}
			mc.stateMu.Unlock()
		}
		mc.mu.Unlock()
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Name < states[j].Name })
	return states
}

// sliceEqual compares two slices for equality.
func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mapEqual compares two maps for equality.
func mapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

type managedClient struct {
	mu         sync.Mutex
	stateMu    sync.Mutex
	generation uint64
	cancel     context.CancelFunc
	lastError  error
	client     *Client
	name       string
	cfg        config.MCPServerConfig
	env        map[string]string
}

// snapshotClient synchronizes the client pointer with replacement and shutdown.
func (mc *managedClient) snapshotClient() *Client {
	mc.stateMu.Lock()
	defer mc.stateMu.Unlock()
	return mc.client
}

// retire fences lookups captured before a config removal or replacement.
// The caller holds mc.mu, so no active call remains.
func (mc *managedClient) retire() {
	mc.stateMu.Lock()
	mc.generation++
	c := mc.client
	mc.stateMu.Unlock()
	c.Stop()
}

// NewPool creates an empty MCP pool. sandboxRoot is the on-disk directory
// for file-tool sandboxing; sandboxEnabled mirrors Tools.Sandbox.Enabled
// in config and gates the per-server launcher (when true, every stdio
// MCP server runs through the landlock+seccomp re-exec).
func NewPool(sandboxRoot string, sandboxEnabled bool, allowUnconfined bool) *Pool {
	return &Pool{
		clients:         make(map[string]*managedClient),
		SandboxRoot:     sandboxRoot,
		Sandbox:         sandboxEnabled,
		AllowUnconfined: allowUnconfined,
	}
}

// RegisterFromConfig registers MCP servers from config.
// Credentials are per-skill env vars injected at process spawn time.
func (p *Pool) RegisterFromConfig(servers map[string]config.MCPServerConfig, credentials map[string]map[string]string) {
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
		c := NewTransportClient(name, cfg, p.SandboxRoot, p.Sandbox, p.AllowUnconfined)
		if creds, ok := credentials[name]; ok {
			c.env = creds
		}
		p.clients[name] = &managedClient{
			client: c,
			name:   name,
			cfg:    cfg,
			env:    c.env,
		}
	}
}

// sandboxDecision encapsulates the logic for determining whether to proceed with sandboxing
// based on kernel support and allow_unconfined configuration.
// Returns: (proceed bool, warn bool, err error)
//   - proceed: whether to proceed with sandboxing
//   - warn: whether to log a warning about the decision
//   - err: error if sandboxing should be denied (fail-closed)
func sandboxDecision(allowUnconfined, landlockOK, seccompOK bool) (proceed bool, warn bool, err error) {
	// Both support present -> (true,false,nil)
	if landlockOK && seccompOK {
		return true, false, nil
	}

	// Support missing AND allowUnconfined=false -> (false,false,<fail-closed error>)
	if !allowUnconfined {
		if !landlockOK {
			return false, false, fmt.Errorf("sandbox: kernel lacks landlock support, refusing to start MCP servers with sandbox enabled (set tools.sandbox.allow_unconfined=true to allow unconfined execution as explicit opt-in)")
		}
		if !seccompOK {
			return false, false, fmt.Errorf("sandbox: kernel lacks seccomp support, refusing to start MCP servers with sandbox enabled (set tools.sandbox.allow_unconfined=true to allow unconfined execution as explicit opt-in)")
		}
	}

	// Support missing AND allowUnconfined=true -> (true,true,nil)
	return true, true, nil
}

// StartAll starts all registered MCP servers and maps their tools by name.
// Failed servers are kept in the map for later retry — not removed.
//
// When p.Sandbox is true (the secure-by-default mode), callers are
// required to be running on a kernel that supports BOTH landlock and
// seccomp-BPF, otherwise every stdio MCP server would launch without
// the syscall-level isolation the rest of the system relies on. The
// probe below fails fast at boot rather than discovering the gap at the
// first tool call.
func (p *Pool) StartAll(ctx context.Context) error {
	if p.Sandbox {
		proceed, warn, err := sandboxDecision(p.AllowUnconfined, checkLandlockSupport(), checkSeccompSupport())
		if err != nil {
			return err
		}
		if warn {
			log.Printf("⚠️  sandbox: kernel lacks support but tools.sandbox.allow_unconfined=true — running MCP servers without OS sandboxing (EXPLICIT OPT-IN, NOT RECOMMENDED FOR PRODUCTION)")
		}
		if proceed {
			log.Printf("sandbox: kernel support verified (landlock + seccomp-BPF) ✅")
		}
	}

	type startEntry struct {
		name       string
		mc         *managedClient
		client     *Client
		generation uint64
	}
	p.mu.RLock()
	var entries []startEntry
	for name, mc := range p.clients {
		if mc == nil || name != mc.name {
			continue
		}
		mc.stateMu.Lock()
		entries = append(entries, startEntry{name, mc, mc.client, mc.generation})
		mc.stateMu.Unlock()
	}
	p.mu.RUnlock()

	// Do not hold the pool lock across startup: StopAll must be able to
	// detach a transport whose initialize handshake is still in progress.
	type aliasEntry struct {
		entry startEntry
		name  string
	}
	var aliases []aliasEntry
	for _, entry := range entries {
		err := entry.client.start(ctx, false)
		entry.mc.stateMu.Lock()
		current := entry.mc.client == entry.client && entry.mc.generation == entry.generation
		if current {
			entry.mc.lastError = err
		}
		entry.mc.stateMu.Unlock()
		if err == nil && current {
			for _, tool := range entry.client.Tools() {
				aliases = append(aliases, aliasEntry{entry, tool.Name})
			}
		}
		if err != nil {
			log.Printf("[mcp-pool] failed to start %s: %v", entry.name, err)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, alias := range aliases {
		entry := alias.entry
		if p.clients[entry.name] != entry.mc {
			continue
		}
		entry.mc.stateMu.Lock()
		current := entry.mc.client == entry.client && entry.mc.generation == entry.generation
		entry.mc.stateMu.Unlock()
		if !current {
			continue
		}
		if _, exists := p.clients[alias.name]; exists {
			log.Printf("[mcp-pool] tool %q shadows server name — skipping alias", alias.name)
			continue
		}
		p.clients[alias.name] = entry.mc
	}
	return nil
}

// toolCallError retains the private cause for errors.Is/As without formatting it
// into messages or logs. A transport error does not prove whether effects occurred.
type toolCallError struct{ cause error }

func (e *toolCallError) Error() string { return "MCP tool outcome uncertain" }
func (e *toolCallError) Unwrap() error { return e.cause }

// CallTool calls a tool by name, lazily starting the server if needed.
// A failed call is never reissued: its effect may already have occurred.
func (p *Pool) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	p.mu.RLock()
	mc, ok := p.clients[name]
	if !ok {
		p.mu.RUnlock()
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	mc.stateMu.Lock()
	generation := mc.generation
	mc.stateMu.Unlock()
	p.mu.RUnlock()
	return p.callManaged(ctx, name, args, mc, generation)
}

// callManaged rejects work that resolved a client before a stop or replacement.
func (p *Pool) callManaged(ctx context.Context, name string, args map[string]any, mc *managedClient, generation uint64) (*mcp.CallToolResult, error) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	mc.stateMu.Lock()
	if mc.generation != generation {
		mc.stateMu.Unlock()
		return nil, errClientStopped
	}
	c := mc.client
	mc.cancel = cancel
	mc.stateMu.Unlock()
	defer func() {
		mc.stateMu.Lock()
		mc.cancel = nil
		mc.stateMu.Unlock()
	}()

	if !c.started() {
		if err := c.start(callCtx, false); err != nil {
			return nil, fmt.Errorf("starting MCP server %q for tool %q: %w", mc.name, name, err)
		}
	}
	result, err := c.CallTool(callCtx, name, args)
	if err == nil {
		return result, nil
	}

	// Never reissue a call whose effects are unknown. Preserve the discovery
	// snapshot for a later, distinct call and retain the original error cause.
	tools := c.Tools()
	c.Stop()
	mc.stateMu.Lock()
	if mc.generation == generation {
		next := NewTransportClient(mc.name, mc.cfg, p.SandboxRoot, p.Sandbox, p.AllowUnconfined)
		next.env = mc.env
		next.tools = tools
		mc.client = next
	}
	mc.stateMu.Unlock()
	return nil, &toolCallError{cause: err}
}

// HasTool returns true if the pool has a handler for the given tool.
func (p *Pool) HasTool(name string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.clients[name]
	return ok
}

// ListTools returns tool names with registered routes, not a reachability check.
func (p *Pool) ListTools() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	seen := make(map[string]bool)
	var names []string
	for _, mc := range p.clients {
		mc.mu.Lock()
		tools := mc.snapshotClient().Tools()
		mc.mu.Unlock()
		for _, tool := range tools {
			// StartAll uses the raw tool.Name from Tools() as the alias key, subject to collisions.
			if p.clients[tool.Name] != mc {
				continue
			}
			if !seen[tool.Name] {
				seen[tool.Name] = true
				names = append(names, tool.Name)
			}
		}
	}
	return names
}

// ToolInfo describes an MCP tool with its schema for registration in the tool registry.
type ToolInfo struct {
	Name        string
	Description string
	InputSchema map[string]any
	ServerName  string
	ScanTarget  string // URL or binary path for security scanning
	// ReadOnly is true when the MCP server declares the tool with a
	// readOnlyHint annotation (it performs no external state mutation).
	// An absent hint stays false: consumers then keep the conservative
	// "changes may have occurred" treatment, so this field can only make
	// failure display more precise, never less truthful.
	ReadOnly bool
}

// ListToolInfos returns metadata for routed tools; a route does not imply reachability.
func (p *Pool) ListToolInfos() []ToolInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	seen := make(map[string]bool)
	var infos []ToolInfo
	for _, mc := range p.clients {
		tools := mc.snapshotClient().Tools()
		cfg := mc.cfg
		name := mc.name
		for _, tool := range tools {
			// StartAll uses the raw tool.Name from Tools() as the alias key, subject to collisions.
			if p.clients[tool.Name] != mc {
				continue
			}
			if seen[tool.Name] {
				continue
			}
			seen[tool.Name] = true
			schema := make(map[string]any)
			if tool.InputSchema.Properties != nil {
				schema["type"] = "object"
				schema["properties"] = tool.InputSchema.Properties
				if len(tool.InputSchema.Required) > 0 {
					schema["required"] = tool.InputSchema.Required
				}
			}
			// ScanTarget: use HTTP/SSE URL if available, else command path
			scanTarget := cfg.URL
			if scanTarget == "" {
				scanTarget = cfg.Command
			}
			infos = append(infos, ToolInfo{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: schema,
				ServerName:  name,
				ScanTarget:  scanTarget,
				ReadOnly:    tool.Annotations.ReadOnlyHint != nil && *tool.Annotations.ReadOnlyHint,
			})
		}
	}
	return infos
}

// StopAll terminates all MCP server connections.
func (p *Pool) StopAll() {
	p.mu.Lock()
	seen := make(map[*managedClient]bool)
	var transports []interface{ Close() error }
	for _, mc := range p.clients {
		if mc == nil || seen[mc] {
			continue
		}
		seen[mc] = true
		mc.stateMu.Lock()
		mc.generation++
		if mc.cancel != nil {
			mc.cancel()
		}
		old := mc.client
		tools := old.Tools()
		if inner := old.detach(); inner != nil {
			transports = append(transports, inner)
		}
		next := NewTransportClient(mc.name, mc.cfg, p.SandboxRoot, p.Sandbox, p.AllowUnconfined)
		next.env = mc.env
		next.tools = tools
		mc.client = next
		mc.stateMu.Unlock()
	}
	// Preserve serialized pool shutdown while closing the detached transports.
	// Close does not need mc.mu, so it can interrupt a blocked stdio write.
	for _, inner := range transports {
		inner.Close()
	}
	p.mu.Unlock()
}

// ReloadReloader adapts per-server reload results to the generic registry.
type ReloadReloader struct{ Pool *Pool }

func (r ReloadReloader) ReloadConfig(cfg *config.Config) error {
	results := r.Pool.ReloadConfig(cfg)
	var errs []error
	for name, err := range results {
		if err != nil {
			errs = append(errs, fmt.Errorf("starting MCP server %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}
