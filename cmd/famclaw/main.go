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

	"github.com/famclaw/famclaw/internal/agent"
	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/browser"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/credstore"
	"github.com/famclaw/famclaw/internal/familystate"
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
	"github.com/famclaw/famclaw/internal/web"
	"github.com/famclaw/famclaw/internal/webfetch"
	"github.com/famclaw/famclaw/internal/websearch"
)

var Version = "dev"

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

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
	if cfg.SecCheck.Enabled && notifier.Len() == 0 && cfg.SecCheck.NotifyOnQuarantine {
		log.Printf("[notify] WARNING: no notification channels enabled but seccheck.notify_on_quarantine=true; parental approvals will fire into the void — add an enabled channel under `notifications:` in your config.yaml (email, slack, discord, sms, ntfy)")
	}
	log.Printf("Notifications: configured (%d channel(s))", notifier.Len())

	// Identity store
	identStore := identity.NewStore(db)
	log.Printf("Identity: ready")

	// MCP tool server pool (stdio, HTTP, SSE transports)
	mcpPool := mcp.NewPool()
	if len(cfg.Skills.MCPServers) > 0 {
		mcpPool.RegisterFromConfig(cfg.Skills.MCPServers, cfg.Skills.Credentials)
		if err := mcpPool.StartAll(context.Background()); err != nil {
			log.Printf("MCP pool: %v", err)
		}
		tools := mcpPool.ListTools()
		log.Printf("MCP: %d servers configured, %d tools available", len(cfg.Skills.MCPServers), len(tools))
	}
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
	reg := skillbridge.NewRegistry(cfg.Skills.Dir, nil, skillbridge.InstallConfig{})
	var enabledSkills []*skillbridge.Skill
	if skills, err := reg.List(); err == nil {
		for _, sk := range skills {
			if reg.IsEnabled(sk.Name) {
				enabledSkills = append(enabledSkills, sk)
				log.Printf("Skill: %s v%s", sk.Name, sk.Version)
			}
		}
	}
	if len(enabledSkills) > 0 {
		log.Printf("Skills: %d loaded for prompt injection", len(enabledSkills))
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
	if cfg.Tools.WebSearch.Enabled {
		builtinTools = append(builtinTools, websearch.Tool(cfg.Tools.WebSearch.AllowedRoles))
		registered = append(registered, "web_search")
	}
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
	chatFn := func(ctx context.Context, user *config.UserConfig, text string) (string, error) {
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
