package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/elastic/go-seccomp-bpf"
	landlock "github.com/landlock-lsm/go-landlock/landlock"
	landlocksyscall "github.com/landlock-lsm/go-landlock/landlock/syscall"

	"github.com/famclaw/famclaw/internal/agent"
	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/browser"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/credstore"
	"github.com/famclaw/famclaw/internal/familystate"
	"github.com/famclaw/famclaw/internal/filetool"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/gateway/discord"
	"github.com/famclaw/famclaw/internal/gateway/telegram"
	"github.com/famclaw/famclaw/internal/gateway/whatsapp"
	"github.com/famclaw/famclaw/internal/honeybadger"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/inference"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/llm/claudecli"
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/skillbridge"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/subagent"
	"github.com/famclaw/famclaw/internal/toolcache"
	"github.com/famclaw/famclaw/internal/usermemory"
	"github.com/famclaw/famclaw/internal/web"
	"github.com/famclaw/famclaw/internal/webfetch"
	"github.com/famclaw/famclaw/internal/websearch"
	"github.com/famclaw/famclaw/internal/todo"
)

// applySandboxRestrictions applies Landlock filesystem restrictions and seccomp network restrictions
func applySandboxRestrictions(sandboxRoot string) error {
	// Apply Landlock filesystem restrictions
	if err := applyLandlockRules(sandboxRoot); err != nil {
		return fmt.Errorf("failed to apply Landlock rules: %w", err)
	}

	// Apply seccomp network restrictions
	if err := applySeccompNetworkFilter(); err != nil {
		return fmt.Errorf("failed to apply seccomp filter: %w", err)
	}

	return nil
}

// applyLandlockRules creates and applies a Landlock ruleset that restricts filesystem access
// to the specified sandbox root (read/write within, nothing outside)
// Also allows execution from standard system paths.
//
// Defensive validation of sandboxRoot happens here too (not just in
// main()) because this function is also called from the sandbox
// launcher sub-process — the launcher is entered by re-exec before
// config is parsed, so it must guard its own input. The checks mirror
// what config.Validate enforces on the host side.
func applyLandlockRules(sandboxRoot string) error {
	cleaned, err := validateSandboxRoot(sandboxRoot)
	if err != nil {
		return err
	}
	sandboxRoot = cleaned

	// Use Landlock V9 with BestEffort for graceful degradation
	// Allow read/write access to sandbox root only
	// Allow execution from standard system paths
	log.Printf("Applying Landlock rules: RWDirs(%s), RODirs(/bin, /usr/bin)", sandboxRoot)
	err = landlock.V9.BestEffort().
		RestrictPaths(
			landlock.RWDirs(sandboxRoot),
			landlock.RODirs("/bin", "/usr/bin"),
		)
	if err != nil {
		log.Printf("Landlock restriction error: %v", err)
		return fmt.Errorf("failed to apply Landlock restrictions: %w", err)
	}
	log.Printf("Landlock rules applied successfully")
	return nil
}

// validateSandboxRoot enforces the preconditions for applying Landlock
// rules: non-empty, absolute, cleaned, not "/" or ".", and an existing
// directory. Returns the cleaned path on success.
//
// Pure function — extracted from applyLandlockRules so unit tests can
// exercise the validation logic without flipping the running process
// into a Landlock-restricted state (which prevents Go's TempDir
// cleanup from removing the test directory).
func validateSandboxRoot(sandboxRoot string) (string, error) {
	if sandboxRoot == "" {
		return "", fmt.Errorf("applyLandlockRules: sandbox root is empty")
	}
	if !filepath.IsAbs(sandboxRoot) {
		return "", fmt.Errorf("applyLandlockRules: sandbox root must be an absolute path, got %q", sandboxRoot)
	}
	cleaned := filepath.Clean(sandboxRoot)
	if cleaned == "/" || cleaned == "." {
		return "", fmt.Errorf("applyLandlockRules: sandbox root must not be \"/\" or \".\", got %q", sandboxRoot)
	}
	info, err := os.Stat(cleaned)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("applyLandlockRules: sandbox root %q does not exist: %w", sandboxRoot, err)
		}
		return "", fmt.Errorf("applyLandlockRules: failed to stat sandbox root %q: %w", sandboxRoot, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("applyLandlockRules: sandbox root %q is not a directory", sandboxRoot)
	}
	return cleaned, nil
}

// applySeccompNetworkFilter applies a seccomp-BPF filter to deny network-related syscalls.
//
// INTENTIONAL DESIGN — network denylist, not syscall denylist. The
// DefaultAction is ActionAllow with only socket/connect/etc. blocked.
// A blanket default-Deny would also block file I/O syscalls — read,
// write, open, mmap, all the things a normal MCP server needs. The
// Landlock ruleset is what contains the filesystem surface; this
// filter is only here to cut off network egress. Reviewers should not
// re-write this as a default-Deny without also re-checking which syscalls
// an MCP child actually invokes.
func applySeccompNetworkFilter() error {
	// Create a seccomp filter that denies network syscalls
	filter := seccomp.Filter{
		NoNewPrivs: true,
		Flag:       seccomp.FilterFlagTSync,
		Policy: seccomp.Policy{
			DefaultAction: seccomp.ActionAllow,
			Syscalls: []seccomp.SyscallGroup{
				{
					Action: seccomp.ActionErrno,
					Names: []string{
						"socket", "socketpair", "bind", "listen", "accept", "accept4",
						"connect", "getsockname", "getpeername", "setsockopt", "getsockopt",
						"sendto", "recvfrom", "sendmsg", "recvmsg",
						"recvmmsg", "sendmmsg",
					},
				},
			},
		},
	}

	// Load and apply the filter
	if err := seccomp.LoadFilter(filter); err != nil {
		return fmt.Errorf("failed to load seccomp filter: %w", err)
	}

	return nil
}

// checkLandlockSupport returns true if Landlock is supported on the current system
func checkLandlockSupport() bool {
	// Try to get the Landlock ABI version - returns error if not supported
	_, err := landlocksyscall.LandlockGetABIVersion()
	return err == nil
}

// checkSeccompSupport returns true if seccomp is supported on the current system
func checkSeccompSupport() bool {
	return seccomp.Supported()
}

// prepareSandboxRoot validates the sandbox root path and ensures the directory exists.
// It returns the cleaned absolute path on success, or an error if the path is invalid
// or the directory cannot be created.
func prepareSandboxRoot(sandboxRoot string) (string, error) {
	// Check for empty string
	if sandboxRoot == "" {
		return "", fmt.Errorf("invalid sandbox root: empty")
	}

	// Reject unsafe values and relative paths
	if !filepath.IsAbs(sandboxRoot) {
		return "", fmt.Errorf("invalid sandbox root %q: must be an absolute path", sandboxRoot)
	}
	if sandboxRoot == "." {
		return "", fmt.Errorf("sandbox root must not be the current directory")
	}
	if sandboxRoot == "/" {
		return "", fmt.Errorf("sandbox root must not be the root directory")
	}

	// Convert to absolute path (does not require path to exist)
	absBase, err := filepath.Abs(sandboxRoot)
	if err != nil {
		return "", fmt.Errorf("invalid sandbox root %q: %w", sandboxRoot, err)
	}

	// Check if absolute path is root directory
	if absBase == "/" {
		return "", fmt.Errorf("sandbox root must not be the root directory")
	}

	// Ensure the sandbox root directory exists
	if err := os.MkdirAll(absBase, 0o700); err != nil {
		return "", fmt.Errorf("failed to create sandbox root %q: %w", absBase, err)
	}

	// Ensure the directory has correct permissions (0o700)
	if err := os.Chmod(absBase, 0o700); err != nil {
		return "", fmt.Errorf("failed to set permissions on sandbox root %q: %w", absBase, err)
	}

	// Verify that what we created is indeed a directory
	info, err := os.Stat(absBase)
	if err != nil {
		return "", fmt.Errorf("failed to stat sandbox root %q: %w", absBase, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("sandbox root %q is not a directory", absBase)
	}

	return absBase, nil
}

// sandboxEnv returns a minimal environment allowlist for the sandbox
// launcher's syscall.Exec call. Only safe, non-secret variables the
// child process actually needs are included.
func sandboxEnv() []string {
	safe := []string{"HOME", "LANG", "PATH", "TERM", "TMPDIR"}
	var out []string
	for _, k := range safe {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

var Version = "dev"

// initMCPPool initializes the MCP server pool.
// Returns the pool and a list of skipped server names.
// Errors from StartAll are non-fatal and logged; the pool is returned
// even if some servers failed to start (caller logs and continues).
func initMCPPool(ctx context.Context, cfg *config.Config, sandboxRoot string) (*mcp.Pool, []string, error) {
	pool := mcp.NewPool(sandboxRoot, cfg.Tools.Sandbox.Enabled)
	var skippedMCPs []string

	if len(cfg.Skills.MCPServers) > 0 {
		pool.RegisterFromConfig(cfg.Skills.MCPServers, cfg.Skills.Credentials)
		if err := pool.StartAll(ctx); err != nil {
			// Sandbox kernel-support probe at pool boot is fail-closed:
			// terminating here keeps an insecure-by-default deployment
			// from silently downgrading to unsandboxed MCP subprocesses.
			// However, make this non-fatal to prevent famclaw from crashing
			// at boot when MCP servers are misconfigured or unreachable.
			if cfg.Tools.Sandbox.Enabled {
				log.Printf("⚠️  MCP pool: %v (non-fatal - continuing boot)", err)
				for serverName := range cfg.Skills.MCPServers {
					skippedMCPs = append(skippedMCPs, fmt.Sprintf("%s (sandbox enabled but failed to start)", serverName))
				}
			} else {
				log.Printf("MCP pool: %v", err)
				for serverName := range cfg.Skills.MCPServers {
					skippedMCPs = append(skippedMCPs, fmt.Sprintf("%s (failed to start)", serverName))
				}
			}
		}
		tools := pool.ListTools()
		log.Printf("MCP: %d servers configured, %d tools available", len(cfg.Skills.MCPServers), len(tools))
	}
	return pool, skippedMCPs, nil
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	// Sandbox launcher mode - detects if we're running as a sandboxed MCP server
	if len(os.Args) >= 3 && os.Args[1] == "-sandbox-launcher" && os.Args[2] == "--sandbox-root" {
		if len(os.Args) < 6 || os.Args[4] != "--" {
			log.Fatalf("Invalid sandbox launcher arguments. Expected: -sandbox-launcher --sandbox-root <root> -- <command> <args...>")
		}
		sandboxRoot := os.Args[3]
		command := os.Args[5]
		if !filepath.IsAbs(command) {
			log.Fatalf("Sandbox launcher: command must be an absolute path: %q", command)
		}
		args := os.Args[6:]

		log.Printf("Sandbox launcher: applying restrictions for root %s, executing %s %v", sandboxRoot, command, args)
		log.Printf("Sandbox launcher: command=%q, args=%q", command, args)
		log.Printf("Sandbox launcher: os.Args=%v", os.Args)

		// Apply Landlock filesystem restrictions
		if err := applySandboxRestrictions(sandboxRoot); err != nil {
			log.Fatalf("Failed to apply sandbox restrictions: %v", err)
		}
		log.Printf("Sandbox launcher: restrictions applied successfully")

		// Execute the real MCP server command
		// Construct full argv for the new process: [command, arg1, arg2, ...]
		var argv []string
		argv = append(argv, command)
		argv = append(argv, args...)
		log.Printf("Sandbox launcher: about to execute %s with argv %v", command, argv)
		// Change working directory to sandbox root
		if err := os.Chdir(sandboxRoot); err != nil {
			log.Fatalf("Failed to change directory to sandbox root %s: %v", sandboxRoot, err)
		}
		// Filter the inherited environment through the sandbox allowlist.
		// Passing os.Environ() would leak FAMCLAW_LLM_API_KEY,
		// FAMCLAW_HMAC_SECRET, *_TOKEN, etc. to every sandbox-launched
		// MCP server. sandboxEnv returns only the safe subset the child
		// actually needs (PATH, HOME, LANG, TERM, TMPDIR).
		execEnv := sandboxEnv()
		if err := syscall.Exec(command, argv, execEnv); err != nil {
			log.Fatalf("Failed to execute %s: %v", command, err)
		}
		// syscall.Exec only returns on error
	}

	// Dispatch subcommands before flag parsing
	if len(os.Args) >= 2 && os.Args[1] == "skill" {
		runSkillCommand(os.Args[2:])
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "policy" {
		runPolicyCommand(os.Args[2:])
		return
	}

	cfgPath := flag.String("config", "config.yaml", "Config file path")
	// seccheck CLI removed — use `honeybadger scan <url>` directly
	_ = flag.String("seccheck", "", "Deprecated: use honeybadger scan instead")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("famclaw %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		return
	}

	// seccheck CLI removed — honeybadger replaces it

	// ── Normal server mode ────────────────────────────────────────────────────
	banner()

	// Config
	cfg, err := config.Load(*cfgPath)
	must(err, "config")

	// Generate secret if not set (first boot)
	if cfg.Server.Secret == "" {
		cfg.Server.Secret = generateSecret()
		log.Printf("Generated server secret (persisted to config file)")
	}

	// Validate config — fail fast with plain-language errors
	if err := cfg.Validate(); err != nil {
		log.Fatalf("Configuration error:\n%v", err)
	}
	if err := cfg.LLM.ValidateProvider(); err != nil {
		log.Fatalf("LLM provider error:\n%v", err)
	}

	log.Printf("Config: %d users, model=%s, addr=%s", len(cfg.Users), cfg.LLM.Model, cfg.Server.Addr())

	// Database
	db, err := store.Open(cfg.Storage.DBPath)
	must(err, "database")
	defer db.Close()
	log.Printf("Database: %s", cfg.Storage.DBPath)

	// OPA policy evaluator
	evaluator, err := policy.NewEvaluator(cfg.Policies.Dir, cfg.Policies.DataDir, cfg.Policies.ExpectedHash)
	must(err, "policy")
	switch {
	case cfg.Policies.Dir != "" && cfg.Policies.DataDir != "":
		log.Printf("Policies: %s (custom override), data: %s", cfg.Policies.Dir, cfg.Policies.DataDir)
	case cfg.Policies.Dir != "":
		log.Printf("Policies: %s (custom override), data: embedded (built-in)", cfg.Policies.Dir)
	case cfg.Policies.DataDir != "":
		log.Printf("Policies: embedded (built-in), data: %s (custom override)", cfg.Policies.DataDir)
	default:
		log.Printf("Policies: embedded (built-in)")
	}
	if cfg.Policies.Dir != "" && hasOnlyBuiltinPolicyNames(cfg.Policies.Dir) {
		log.Printf("WARN: policies.dir %q contains only files with the same names as the built-in "+
			"policies. If you did not intend a custom override, remove the policies: block from "+
			"config.yaml to use the embedded versions.", cfg.Policies.Dir)
	}

	// Query classifier
	clf := classifier.New()

	// Inference sidecar (optional — llama-server managed by FamClaw)
	var sidecar *inference.Sidecar
	if cfg.Inference.Backend == "llama-server" {
		sidecar = inference.NewSidecar(inference.SidecarConfig{
			BinaryPath: cfg.Inference.Binary,
			ModelPath:  cfg.Inference.ModelPath,
			Port:       cfg.Inference.Port,
			GPULayers:  cfg.Inference.GPULayers,
			ExtraArgs:  cfg.Inference.ExtraArgs,
		})
		if err := sidecar.Start(context.Background()); err != nil {
			log.Fatalf("Failed to start llama-server: %v", err)
		}
		defer sidecar.Stop()
		log.Printf("Inference: llama-server starting on port %d", cfg.Inference.Port)
		if err := sidecar.WaitReady(context.Background(), 60*time.Second); err != nil {
			log.Printf("⚠️  llama-server not ready: %v", err)
		} else {
			log.Printf("Inference: llama-server ready ✅")
		}
		// Override LLM base URL to point at the sidecar
		if cfg.LLM.BaseURL == "" {
			cfg.LLM.BaseURL = sidecar.BaseURL()
		}
	}

	// LLM health check (skip for claude_cli — no HTTP endpoint to ping)
	if cfg.LLM.Provider != "claude_cli" {
		hcEP := cfg.LLMEndpointFor(nil)
		llmClient := llm.NewClient(hcEP.BaseURL, hcEP.Model, hcEP.APIKey)
		ctx5s, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := llmClient.Ping(ctx5s); err != nil {
			log.Printf("⚠️  LLM not ready: %v", err)
			log.Printf("   Run: ollama pull %s", cfg.LLM.Model)
		} else {
			log.Printf("LLM: %s @ %s ✅", hcEP.Model, hcEP.BaseURL)
		}
		cancel()
	}

	// Notifications
	notifier := notify.NewMultiNotifier(cfg.Notifications, cfg.Server.Secret)

	// Check for potential silent notification failures
	if notifier.Len() == 0 {
		// Check if any user has parent role
		hasParent := false
		for _, user := range cfg.Users {
			if user.Role == "parent" {
				hasParent = true
				break
			}
		}

		// Log warning if notifications are expected but no channels are configured
		if hasParent || cfg.SecCheck.NotifyOnQuarantine {
			log.Printf("[notify] WARNING: no notification channels enabled but parent users or seccheck.notify_on_quarantine are configured; parental approvals will fire into the void — add an enabled channel under `notifications:` in your config.yaml (email, slack, discord, sms, ntfy)")
		}
	}

	log.Printf("Notifications: configured (%d channel(s))", notifier.Len())

	// Identity store
	identStore := identity.NewStore(db)
	log.Printf("Identity: ready")
	// MCP tool server pool (stdio, HTTP, SSE transports)
	var sandboxRoot string
	if cfg.Tools.SandboxRoot != "" {
		sandboxRoot = cfg.Tools.SandboxRoot
	} else {
		// Default to a subdirectory of the directory containing the DB file
		dbDir := filepath.Dir(cfg.Storage.DBPath)
		sandboxRoot = filepath.Join(dbDir, "skill_sandbox")
	}

	// Reject unsafe values
	if sandboxRoot == "." || sandboxRoot == "/" {
		log.Fatalf("Sandbox root must not be the current or root directory")
	}
	sandboxRoot, err = prepareSandboxRoot(sandboxRoot)
	if err != nil {
		log.Fatalf("Invalid sandbox root: %v", err)
	}

	// Validate that the sandbox root is not a parent of itself
	// This ensures we properly validate the path and avoid traversal issues
	if err := cfg.Validate(); err != nil {
		log.Fatalf("Configuration validation failed: %v", err)
	}
	var mcpPool *mcp.Pool
	mcpPool, _, _ = initMCPPool(context.Background(), cfg, sandboxRoot)
	defer mcpPool.StopAll()

	// HoneyBadger client + quarantine for runtime scanning
	var hbScanner skillbridge.Scanner
	var quarantine *skillbridge.Quarantine
	if cfg.SecCheck.Enabled && cfg.SecCheck.RuntimeScan {
		hb := honeybadger.New()
		if hb.Available() {
			hbScanner = hb
			quarantine = skillbridge.NewQuarantine(db)
			if err := quarantine.Load(context.Background()); err != nil {
				log.Printf("⚠️  Failed to load quarantine: %v", err)
			} else {
				log.Printf("Security: quarantine loaded (%d blocked)", quarantine.Len())
			}
			log.Printf("Security: runtime scanning enabled (honeybadger available) ✅")
		} else {
			log.Printf("⚠️  Runtime scanning enabled in config but honeybadger binary not in PATH")
			log.Printf("   Install: go install github.com/famclaw/honeybadger/cmd/honeybadger@latest")
			log.Printf("   Or disable: seccheck.runtime_scan: false in config.yaml")
		}
	}

	// Skills loaded for prompt injection (independent of MCP)
	reg := skillbridge.NewRegistry(cfg.Skills.Dir, hbScanner, skillbridge.InstallConfig{
		Enabled:      cfg.SecCheck.Enabled,
		AutoSecCheck: cfg.SecCheck.AutoSecCheck,
		BlockOnFail:  cfg.SecCheck.BlockOnFail,
		Paranoia:     cfg.SecCheck.Paranoia,
	}, cfg.Skills.RoleEnablement)
	var enabledSkills []*skillbridge.Skill
	// Fallback to global enabled skills (no user context at startup).
	if skills, err := reg.List(); err == nil {
		for _, sk := range skills {
			if reg.IsEnabled(sk.Name) {
				enabledSkills = append(enabledSkills, sk)
				log.Printf("Skill: %s v%s", sk.Name, sk.Version)
			}
		}
	}

	// Subagent scheduler for spawn_agent dispatching (max 2 concurrent)
	agentScheduler := subagent.NewScheduler(2)

	// Phase 2 — tool result spillover cache. See spec §3.
	// Disabled fall-back: nil cache, agent runs the legacy inline-everything
	// path (vulnerable to overflow on big tool results — the v0.5.9 bug).
	var toolCache *toolcache.Cache
	if cfg.Tools.ToolCache.Enabled || cfg.Tools.WebFetch.Enabled {
		// Auto-enable when web_fetch is on — they are paired features.
		cacheCfg := toolcache.Config{
			DB:         db.SQL(),
			CacheDir:   cfg.Tools.ToolCache.CacheDir,
			TTLDefault: 6 * time.Hour,
			PerUserCap: cfg.Tools.ToolCache.PerUserCapMB * 1024 * 1024,
			TTLByRole:  parseTTLByRole(cfg.Tools.ToolCache.TTLByRole),
		}
		if cacheCfg.PerUserCap == 0 {
			cacheCfg.PerUserCap = 100 * 1024 * 1024 // 100MB default
		}
		tc, err := toolcache.New(cacheCfg)
		if err != nil {
			log.Fatalf("toolcache: %v", err)
		}
		if err := tc.Reconcile(context.Background()); err != nil {
			log.Printf("toolcache reconcile: %v", err)
		}
		interval := 15 * time.Minute
		if cfg.Tools.ToolCache.SweepInterval != "" {
			if d, err := time.ParseDuration(cfg.Tools.ToolCache.SweepInterval); err == nil {
				interval = d
			}
		}
		tc.StartSweeper(interval)
		defer tc.StopSweeper()
		toolCache = tc
		log.Printf("ToolCache: enabled (per-user cap=%dMB, sweep=%s)",
			cacheCfg.PerUserCap/(1024*1024), interval)
	}

	// Builtin tools available to the LLM
	builtinTools := []agentcore.Tool{subagent.SpawnAgentTool()}
	registered := []string{"spawn_agent"}
	if cfg.Tools.WebFetch.Enabled {
		builtinTools = append(builtinTools, webfetch.Tool(cfg.Tools.WebFetch.AllowedRoles))
		registered = append(registered, "web_fetch")
		if toolCache != nil {
			builtinTools = append(builtinTools, toolcache.Tool(cfg.Tools.WebFetch.AllowedRoles))
			registered = append(registered, "tool_result_more")
		}
	}
	// Phase 3.3 — get_family_state + propose_family_fact are always
	// available; OPA policy already permits them for every role, and the
	// handlers degrade gracefully when the store is nil (tests).
	builtinTools = append(builtinTools, familystate.GetTool(), familystate.ProposeTool())
	registered = append(registered, "get_family_state", "propose_family_fact")

// Todo tool — always available for all roles; access controlled by policy if needed.
	builtinTools = append(builtinTools, todo.Tool(nil))
	registered = append(registered, "todo")
	
	// Phase 4 — user memory tools (remember/recall/forget) available to all roles.
	builtinTools = append(builtinTools,
		usermemory.RememberDefinition(),
		usermemory.RecallDefinition(),
		usermemory.ForgetDefinition(),
	)
	registered = append(registered, "remember_user_memory", "recall_user_memory", "forget_user_memory")
	if cfg.Tools.WebSearch.Enabled {
		builtinTools = append(builtinTools, websearch.Tool(cfg.Tools.WebSearch.AllowedRoles))
		registered = append(registered, "web_search")
	}
	// File tools are always available; access is restricted by OPA policy.
	builtinTools = append(builtinTools,
		filetool.FileReadTool(),
		filetool.FileWriteTool(),
		filetool.FileStatTool(),
		filetool.FileListTool(),
	)
	registered = append(registered, "file_read", "file_write", "file_stat", "file_list")
	var browserPool *browser.Pool
	if cfg.Tools.Browser.Enabled {
		// Pool owns its idle-sweeper goroutine; pass Background and rely on
		// Close (deferred below) to cancel it. A process-wide cancellable
		// ctx exists later in main but Pool boots before the gateway ctx.
		pool, err := browser.NewPool(context.Background(), browser.Config{
			Endpoint:         cfg.Tools.Browser.Endpoint,
			IdleTimeout:      time.Duration(cfg.Tools.Browser.IdleSec) * time.Second,
			SnapshotMaxChars: cfg.Tools.Browser.SnapshotMaxChars,
		})
		if err != nil {
			log.Fatalf("Browser pool: %v", err)
		}
		defer pool.Close()
		browserPool = pool
		for _, t := range browser.Tools(cfg.Tools.Browser.AllowedRoles) {
			builtinTools = append(builtinTools, t)
			registered = append(registered, strings.TrimPrefix(t.Name, "builtin__"))
		}
		log.Printf("Browser: enabled (endpoint=%s, idle=%ds)", cfg.Tools.Browser.Endpoint, cfg.Tools.Browser.IdleSec)
	}
	log.Printf("Builtin tools: %d registered (%s)", len(builtinTools), strings.Join(registered, ", "))

	// Chat function for gateway router
	chatFn := func(ctx context.Context, user *config.UserConfig, text string, msgCtx gateway.MsgContext) (string, error) {
		var llmClient llm.Chatter
		switch cfg.LLM.Provider {
		case "claude_cli":
			llmClient = claudecli.New()
		default: // "" or "openai"
			ep := cfg.LLMEndpointFor(user)
			llmClient = llm.NewClient(ep.BaseURL, ep.Model, ep.APIKey)
		}
		a := agent.NewAgent(user, cfg, llmClient, evaluator, clf, db, agent.AgentDeps{
			Pool:         mcpPool,
			Skills:       enabledSkills,
			Quarantine:   quarantine,
			Scanner:      hbScanner,
			Scheduler:    agentScheduler,
			BuiltinTools: builtinTools,
			Cache:        toolCache,
			BrowserPool:  browserPool,
		})
		resp, err := a.Chat(ctx, text, nil)
		if err != nil {
			return "", err
		}
		return resp.Content, nil
	}

	// Gateway lifecycle context — shared between the session pool and bots.
	// Cancelled during graceful shutdown so session goroutines exit cleanly.
	gwCtx, gwCancel := context.WithCancel(context.Background())
	defer gwCancel()

	// Gateway router
	router := gateway.NewRouter(gwCtx, cfg, identStore, clf, evaluator, db, notifier, chatFn, reg)

	// Gateway bots
	var gateways []gateway.Gateway
	if cfg.Gateways.Telegram.Enabled && cfg.Gateways.Telegram.Token != "" {
		gateways = append(gateways, telegram.New(cfg.Gateways.Telegram.Token))
		log.Printf("Gateway: Telegram enabled")
	}
	if cfg.Gateways.Discord.Enabled && cfg.Gateways.Discord.Token != "" {
		gateways = append(gateways, discord.New(cfg.Gateways.Discord.Token))
		log.Printf("Gateway: Discord enabled")
	}
	if cfg.Gateways.WhatsApp.Enabled {
		gateways = append(gateways, whatsapp.New(cfg.Gateways.WhatsApp.DBPath))
		log.Printf("Gateway: WhatsApp enabled (placeholder)")
	}

	if len(gateways) > 0 {
		gateway.StartAll(gwCtx, gateways, router.Handle)
		log.Printf("Gateways: %d started", len(gateways))
	}

	// Session store + credential vault for the web admin auth flow.
	// SessionStore needs the raw *sql.DB underlying store.DB. The vault is
	// keyed off /etc/machine-id (or the platform-specific equivalent); a
	// failure here means the host has no stable identifier — fail fast since
	// nothing in the auth flow can succeed without it.
	sessions := store.NewSessionStore(db.SQL())
	machineID, midErr := credstore.MachineID()
	if midErr != nil {
		log.Fatalf("FATAL [machine-id]: %v", midErr)
	}
	vault, err := credstore.New(machineID)
	if err != nil {
		log.Fatalf("FATAL [vault]: %v", err)
	}

	// Vault-mismatch probe. If a parent_pin row exists but cannot be decrypted
	// with the current machine-bound key, the binary is running on different
	// hardware (or against a copied database). Surface this to the web server
	// so the UI shows the unlock page instead of failing every login with a
	// generic 401. First boot (no PIN row yet) is not a mismatch — the
	// bootstrap wizard handles it.
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startupCancel()
	var vaultMismatch bool
	var pinCT []byte
	pinErr := db.SQL().QueryRowContext(startupCtx,
		`SELECT ciphertext FROM vault_secrets WHERE name = 'parent_pin'`).Scan(&pinCT)
	switch {
	case errors.Is(pinErr, sql.ErrNoRows):
		// First boot — no PIN yet; vault is not yet bound.
		vaultMismatch = false
	case pinErr != nil:
		log.Fatalf("reading vault_secrets: %v", pinErr)
	default:
		// PIN exists — try decrypting with the current machine key.
		_, decErr := vault.Decrypt(pinCT)
		switch {
		case decErr == nil:
			vaultMismatch = false
		case errors.Is(decErr, credstore.ErrMachineMismatch):
			vaultMismatch = true
			log.Printf("[WARN] machine fingerprint changed; vault locked; web UI will show unlock page")
		default:
			log.Fatalf("vault decrypt error (possible corruption): %v", decErr)
		}
	}

	// Web server
	srv := web.NewServer(cfg, *cfgPath, db, sessions, vault, identStore, evaluator, clf, notifier, enabledSkills, reg, mcpPool)
	srv.SetVaultMismatch(vaultMismatch)
	httpSrv := &http.Server{
		Addr:         cfg.Server.Addr(),
		Handler:      srv.Handler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 10 * time.Minute, // streaming LLM responses
		IdleTimeout:  120 * time.Second,
	}

	// mDNS removed in v0.5.x — see #110. Use the device IP address.

	// Run a one-shot cleanup of expired web sessions at startup so a long-idle
	// box doesn't carry yesterday's stale rows into today's first request.
	if deleted, err := sessions.DeleteExpired(startupCtx); err != nil {
		log.Printf("[session-cleanup] startup error: %v", err)
	} else if deleted > 0 {
		log.Printf("[session-cleanup] deleted %d expired sessions at startup", deleted)
	}

	// Hourly background sweep. The session middleware filters expired rows on
	// read, so the worst-case effect of a missed tick is a transiently larger
	// table — never an auth bypass. Cancelled on shutdown so the goroutine
	// exits before the process does.
	sessionCleanupCtx, sessionCleanupCancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-sessionCleanupCtx.Done():
				return
			case <-ticker.C:
				deleted, err := sessions.DeleteExpired(sessionCleanupCtx)
				if err != nil {
					log.Printf("[session-cleanup] error: %v", err)
				} else if deleted > 0 {
					log.Printf("[session-cleanup] deleted %d expired sessions", deleted)
				}
			}
		}
	}()

	// Start
	go func() {
		log.Printf("✅ FamClaw %s listening on %s", Version, cfg.Server.Addr())
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server: %v", err)
		}
	}()

	printStartGuide(cfg)

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down…")
	gwCancel()             // stop gateway bots + cancel session pool shutdownCtx
	router.Shutdown()      // cancel in-flight session processing
	sessionCleanupCancel() // stop session-cleanup goroutine

	ctx, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel2()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	log.Println("Stopped.")
}

// runSecCheck removed — honeybadger replaces seccheck.
// Use: honeybadger scan <repo-url>

func banner() {
	fmt.Printf(`
  ███████╗ █████╗ ███╗   ███╗ ██████╗██╗      █████╗ ██╗    ██╗
  ██╔════╝██╔══██╗████╗ ████║██╔════╝██║     ██╔══██╗██║    ██║
  █████╗  ███████║██╔████╔██║██║     ██║     ███████║██║ █╗ ██║
  ██╔══╝  ██╔══██║██║╚██╔╝██║██║     ██║     ██╔══██║██║███╗██║
  ██║     ██║  ██║██║ ╚═╝ ██║╚██████╗███████╗██║  ██║╚███╔███╔╝
  ╚═╝     ╚═╝  ╚═╝╚═╝     ╚═╝ ╚═════╝╚══════╝╚═╝  ╚═╝ ╚══╝╚══╝
  Family AI Assistant • Version %s • %s/%s
`, Version, runtime.GOOS, runtime.GOARCH)
}

func printStartGuide(cfg *config.Config) {
	fmt.Printf(`
────────────────────────────────────────────────────────
  Open FamClaw on any device on your network:

  🖥️  http://localhost:%d

  Find this device's IP with:
    Mac:   ipconfig getifaddr en0
    Linux: hostname -I | awk '{print $1}'

  Then open http://<IP>:%d on any device on your LAN.
────────────────────────────────────────────────────────
`, cfg.Server.Port, cfg.Server.Port)
}

func generateSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("FATAL: crypto/rand failed: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func must(err error, context string) {
	if err != nil {
		log.Fatalf("FATAL [%s]: %v", context, err)
	}
}

// hasOnlyBuiltinPolicyNames reports whether dir contains exactly the
// filenames of the built-in policies (and nothing else). This is a
// filename-only heuristic — it does not compare file contents — so the
// caller phrases its warning carefully ("same names as", not "mirrors").
func hasOnlyBuiltinPolicyNames(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	regoFiles := make(map[string]bool)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			return false
		}
		if filepath.Ext(name) == ".rego" {
			regoFiles[name] = true
		}
	}
	builtin := map[string]bool{"decision.rego": true, "tool_policy.rego": true}
	if len(regoFiles) != len(builtin) {
		return false
	}
	for name := range builtin {
		if !regoFiles[name] {
			return false
		}
	}
	return true
}

// parseTTLByRole converts the YAML string-keyed durations to typed
// durations. Unparseable values are skipped silently (the cache falls
// back to Config.TTLDefault for missing roles).
func parseTTLByRole(in map[string]string) map[string]time.Duration {
	if len(in) == 0 {
		// Family-bot defaults when no config block present. See spec §3.
		return map[string]time.Duration{
			"parent":    24 * time.Hour,
			"age_13_17": 6 * time.Hour,
			"age_8_12":  1 * time.Hour,
			"under_8":   30 * time.Minute,
		}
	}
	out := make(map[string]time.Duration, len(in))
	for role, s := range in {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			out[role] = d
		}
	}
	return out
}
