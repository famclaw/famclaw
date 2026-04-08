package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/famclaw/famclaw/internal/agent"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/gateway/discord"
	"github.com/famclaw/famclaw/internal/gateway/telegram"
	"github.com/famclaw/famclaw/internal/gateway/whatsapp"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/mdns"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/honeybadger"
	"github.com/famclaw/famclaw/internal/inference"
	"github.com/famclaw/famclaw/internal/skillbridge"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/web"
)

var Version = "dev"

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	// Dispatch subcommands before flag parsing
	if len(os.Args) >= 2 && os.Args[1] == "skill" {
		runSkillCommand(os.Args[2:])
		return
	}

	cfgPath  := flag.String("config", "config.yaml", "Config file path")
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

	log.Printf("Config: %d users, model=%s, addr=%s", len(cfg.Users), cfg.LLM.Model, cfg.Server.Addr())

	// Database
	db, err := store.Open(cfg.Storage.DBPath)
	must(err, "database")
	defer db.Close()
	log.Printf("Database: %s", cfg.Storage.DBPath)

	// OPA policy evaluator
	evaluator, err := policy.NewEvaluator(cfg.Policies.Dir, cfg.Policies.DataDir)
	must(err, "policy")
	log.Printf("Policies: %s", cfg.Policies.Dir)

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

	// LLM health check
	hcBaseURL, hcModel, hcAPIKey := cfg.LLMClientFor(nil)
	llmClient := llm.NewClient(hcBaseURL, hcModel, hcAPIKey)
	ctx5s, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := llmClient.Ping(ctx5s); err != nil {
		log.Printf("⚠️  LLM not ready: %v", err)
		log.Printf("   Run: ollama pull %s", cfg.LLM.Model)
	} else {
		log.Printf("LLM: %s @ %s ✅", cfg.LLM.Model, cfg.LLM.BaseURL)
	}
	cancel()

	// Notifications
	notifier := notify.NewMultiNotifier(cfg.Notifications, cfg.Server.Secret)
	log.Printf("Notifications: configured")

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

	// OAuth token store for subscription-based auth (Anthropic Claude)
	oauthStorePath := filepath.Join(filepath.Dir(*cfgPath), "oauth-tokens.json")
	oauthStore := llm.NewOAuthStore(oauthStorePath, llm.DefaultTokenURL, llm.DefaultClientID)
	if oauthStore.HasToken("anthropic") {
		log.Printf("OAuth: Anthropic token loaded (auto-refresh enabled)")
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

	// Chat function for gateway router
	chatFn := func(ctx context.Context, user *config.UserConfig, text string) (string, error) {
		ep := cfg.LLMEndpointFor(user)
		var client *llm.Client
		if ep.AuthType == "oauth" {
			client = llm.NewOAuthClient(ep.BaseURL, ep.Model, oauthStore, "anthropic")
		} else {
			client = llm.NewClient(ep.BaseURL, ep.Model, ep.APIKey)
		}
		a := agent.NewAgent(user, cfg, client, evaluator, clf, db)
		a.SetPool(mcpPool)
		a.SetSkills(enabledSkills)
		a.SetQuarantine(quarantine)
		a.SetScanner(hbScanner)
		a.SetOAuthStore(oauthStore)
		resp, err := a.Chat(ctx, text, nil)
		if err != nil {
			return "", err
		}
		return resp.Content, nil
	}

	// Gateway router
	router := gateway.NewRouter(cfg, identStore, clf, evaluator, db, notifier, chatFn)

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

	gwCtx, gwCancel := context.WithCancel(context.Background())
	defer gwCancel()
	if len(gateways) > 0 {
		gateway.StartAll(gwCtx, gateways, router.Handle)
		log.Printf("Gateways: %d started", len(gateways))
	}

	// Web server
	srv := web.NewServer(cfg, *cfgPath, db, evaluator, clf, notifier, enabledSkills, mcpPool, oauthStore)
	httpSrv := &http.Server{
		Addr:         cfg.Server.Addr(),
		Handler:      srv.Handler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 10 * time.Minute, // streaming LLM responses
		IdleTimeout:  120 * time.Second,
	}

	// mDNS — advertise on local network as famclaw.local
	go mdns.Advertise(cfg.Server.MDNSName, cfg.Server.Port)

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
	gwCancel() // stop gateway bots

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

  📱 http://%s.local:%d
  🖥️  http://localhost:%d

  Or find this device's IP with:
    Mac:   ipconfig getifaddr en0
    Linux: hostname -I | awk '{print $1}'

  Then open http://<IP>:%d on any device.
────────────────────────────────────────────────────────
`, cfg.Server.MDNSName, cfg.Server.Port, cfg.Server.Port, cfg.Server.Port)
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

func min(a, b int) int {
	if a < b { return a }
	return b
}
