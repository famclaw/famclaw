// Package web provides the FamClaw HTTP server.
// Serves the embedded web UI and a REST+WebSocket API.
// All local — no CDN dependencies, works offline on RPi/old phones.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/famclaw/famclaw/internal/agent"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/credstore"
	"github.com/famclaw/famclaw/internal/familystate"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/llm/claudecli"
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/skillbridge"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/web/middleware"
	"github.com/gorilla/websocket"
)

//go:embed static
var staticFiles embed.FS

// Server is the FamClaw web server.
type Server struct {
	cfg           *config.Config
	cfgPath       string // path to config.yaml for settings API
	db            *store.DB
	identStore    *identity.Store
	evaluator     *policy.Evaluator
	clf           *classifier.Classifier
	notifier      *notify.MultiNotifier
	skills        []*skillbridge.Skill  // injected into agent system prompt
	skillRegistry *skillbridge.Registry // backs POST /api/skills/install + /remove
	pool          *mcp.Pool             // MCP tool pool for agent tool calls
	familyState   *familystate.Store    // Phase 3.3 — nil disables /api/family-state/*
	staticHandler http.Handler          // embedded static file server
	upgrader      websocket.Upgrader
	cfgMu         sync.RWMutex               // guards cfg during settings reads/writes
	clients       map[*websocket.Conn]string // conn → userName
	clientsMu     sync.RWMutex

	// Session-based auth wiring (Phase 6).
	sessions      *store.SessionStore
	vault         *credstore.Vault
	auth          *AuthHandler
	vaultMismatch bool // protected by vaultMu — true once a probe finds the on-disk vault was sealed by a different machine
	vaultMu       sync.RWMutex
}

// wsMessage is a WebSocket protocol message.
type WsMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func NewServer(cfg *config.Config, cfgPath string, db *store.DB, sessions *store.SessionStore, vault *credstore.Vault,
	identStore *identity.Store, evaluator *policy.Evaluator, clf *classifier.Classifier, notifier *notify.MultiNotifier,
	skills []*skillbridge.Skill, skillRegistry *skillbridge.Registry, pool *mcp.Pool) *Server {
	var fs *familystate.Store
	if db != nil {
		fs = familystate.NewStore(db)
	}
	s := &Server{
		cfg:           cfg,
		cfgPath:       cfgPath,
		db:            db,
		sessions:      sessions,
		vault:         vault,
		identStore:    identStore,
		evaluator:     evaluator,
		clf:           clf,
		notifier:      notifier,
		pool:          pool,
		skills:        skills,
		skillRegistry: skillRegistry,
		familyState:   fs,
		clients:       make(map[*websocket.Conn]string),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				// Allow connections from LAN — all origins on local network
				return true
			},
		},
	}
	// Auth handler is wired with closures so the dependency graph stays
	// one-directional: AuthHandler does not import *Server, only the bits of
	// state it needs.
	if sessions != nil && vault != nil {
		s.auth = NewAuthHandler(sessions, vault, s.getParentPINCiphertext, s.resolveParentUserID)
	}
	return s
}

// Handler returns the root HTTP handler.
//
// Routing layers:
//   - Always-public: /login, /logout, /session, /api/health, /decide
//     (HMAC-signed approval token), the static asset tree, the setup-wizard
//     endpoints used before any PIN exists.
//   - Protected: every admin surface plus /api/chat (settings, approvals,
//     skills, unknown accounts, conversations, SSE stream, gateway tester
//     probes, web-UI chat WebSocket). Wrapped in s.protect(...), which
//     delegates to middleware.WithSession — no handler re-checks auth.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ── Static files (embedded web UI) ────────────────────────────────────────
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("static files: %v", err)
	}
	s.staticHandler = http.FileServer(http.FS(staticFS))
	// Root: redirect to /setup if unconfigured, otherwise serve static files.
	// /login and /unlock are also served from the static tree (Phase 8 adds
	// the actual HTML files; until then they 404, which is acceptable for the
	// transition window).
	mux.HandleFunc("/", s.handleRoot)

	// ── Auth (always public) ──────────────────────────────────────────────────
	if s.auth != nil {
		mux.HandleFunc("/login", s.auth.HandleLogin)
		mux.HandleFunc("/logout", s.auth.HandleLogout)
		mux.HandleFunc("/session", s.auth.HandleSession)
	}

	// ── First-boot setup endpoints (always public) ────────────────────────────
	// /api/setup/detect runs before any PIN is configured (see handleSetupDetect
	// for its own first-boot guard). The PIN-bootstrap and vault-unlock endpoints
	// are stubs in Phase 6 and get fleshed out in Phase 7.
	mux.HandleFunc("/api/setup/detect", s.handleSetupDetect)
	mux.HandleFunc("/api/setup/pin", s.handleSetupPIN)
	mux.HandleFunc("/api/setup/unlock", s.handleSetupUnlock)
	mux.HandleFunc("/api/health", s.handleHealth)

	// ── Gateway / external entry points (their own auth) ──────────────────────
	mux.HandleFunc("/decide", s.handleDecideLink) // HMAC-signed approval token

	mux.HandleFunc("/api/chat", s.handleChat) // WebSocket — public, user identity from ?user=NAME query (gateway model, not session)

	// ── Protected admin surface (session-gated) ───────────────────────────────
	mux.Handle("/api/users", s.protect(s.handleUsers))
	mux.Handle("/api/approvals", s.protect(s.handleApprovals))
	mux.Handle("/api/approvals/decide", s.protect(s.handleDecide))
	mux.Handle("/api/skills", s.protect(s.handleSkills))
	mux.Handle("/api/skills/install", s.protect(s.handleSkillInstall))
	mux.Handle("/api/skills/remove", s.protect(s.handleSkillRemove))
		mux.Handle("/api/mcp", s.protect(s.handleMCP))
		mux.Handle("/api/mcp/add", s.protect(s.handleMCPAdd))
		mux.Handle("/api/mcp/remove", s.protect(s.handleMCPRemove))
	mux.Handle("/api/unknown-accounts", s.protect(s.handleUnknownAccounts))
	mux.Handle("/api/unknown-accounts/link", s.protect(s.handleUnknownAccountLink))
	mux.Handle("/api/unknown-accounts/dismiss", s.protect(s.handleUnknownAccountDismiss))
	mux.Handle("/api/conversations", s.protect(s.handleConversations))
	mux.Handle("/api/family-state/facts", s.protect(s.handleFamilyStateFacts))
	mux.Handle("/api/family-state/facts/", s.protect(s.handleFamilyStateFact))
	mux.Handle("/api/family-state/categories", s.protect(s.handleFamilyStateCategories))
	mux.Handle("/api/family-state/categories/", s.protect(s.handleFamilyStateCategory))
	mux.Handle("/api/settings", s.protect(s.handleSettings))
	mux.Handle("/api/setup/test-telegram", s.conditionalProtect(s.handleTestTelegram))
	mux.Handle("/api/setup/test-discord", s.conditionalProtect(s.handleTestDiscord))
	mux.Handle("/api/stream", s.protect(s.handleStream))

	return mux
}

// protect wraps a handler with the session-validation middleware. Every admin
// route in Handler() goes through this — handlers themselves never re-check
// auth, so removing the middleware would silently expose them.
func (s *Server) protect(h http.HandlerFunc) http.Handler {
	if s.sessions == nil {
		// Defensive: a Server constructed without sessions (test fixtures, or
		// a misconfigured boot path) should fail closed rather than serve the
		// admin surface unauthenticated.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthenticated"}` + "\n"))
		})
	}
	return middleware.WithSession(s.sessions)(h)
}

// conditionalProtect bypasses session protection while the parent PIN has not
// been configured yet, and falls back to s.protect once setup is complete.
// Used by setup-wizard endpoints (gateway "Test connection" probes) that the
// wizard hits before /api/setup/pin seats the initial session cookie — without
// this gate they would 401 on every fresh install.
func (s *Server) conditionalProtect(h http.HandlerFunc) http.Handler {
	protected := s.protect(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hasPINConfigured(r.Context()) {
			h(w, r)
			return
		}
		protected.ServeHTTP(w, r)
	})
}

// getParentPINCiphertext fetches the encrypted parent PIN hash from the
// vault_secrets table. Returns sql.ErrNoRows when no PIN has been configured;
// AuthHandler maps that to the same generic 401 as a wrong-PIN attempt so
// callers cannot distinguish the two.
func (s *Server) getParentPINCiphertext(ctx context.Context) ([]byte, error) {
	var ct []byte
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT ciphertext FROM vault_secrets WHERE name = 'parent_pin'`).Scan(&ct)
	return ct, err
}

// hasPINConfigured reports whether a parent_pin row exists in the vault. Used
// by the first-boot wizard to choose between the PIN-bootstrap flow and the
// unlock flow. Errors are logged and treated as "not configured" so the UI
// gracefully falls through to the bootstrap path on a transient DB hiccup.
func (s *Server) hasPINConfigured(ctx context.Context) bool {
	var n int
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM vault_secrets WHERE name = 'parent_pin'`).Scan(&n)
	if err != nil {
		log.Printf("hasPINConfigured: %v", err)
		return false
	}
	return n > 0
}

// resolveParentUserID returns a stable numeric ID for the first parent user.
// Users live in YAML config (no DB users table), so we synthesise the ID from
// the config-order index of the first parent. This is fine for session
// accounting (web_sessions.user_id is informational), and a future `users`
// table can drop in without changing the closure signature.
func (s *Server) resolveParentUserID(ctx context.Context) (int64, error) {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	for i, u := range s.cfg.Users {
		if u.Role == "parent" {
			return int64(i + 1), nil
		}
	}
	return 0, fmt.Errorf("resolving parent user id: no parent user configured")
}

// getVaultMismatch returns the current vault-mismatch flag. Used by handleRoot
// to decide whether to redirect into the /unlock page, and by handleSetupUnlock
// to short-circuit the rebind endpoint when no mismatch is in flight.
func (s *Server) getVaultMismatch() bool {
	s.vaultMu.RLock()
	defer s.vaultMu.RUnlock()
	return s.vaultMismatch
}

// SetVaultMismatch sets the vault-mismatch flag. main.go calls this once at
// startup after probing the on-disk PIN ciphertext against the current
// machine-bound key; the unlock handler clears it back to false after a
// successful re-bind.
func (s *Server) SetVaultMismatch(v bool) {
	s.vaultMu.Lock()
	s.vaultMismatch = v
	s.vaultMu.Unlock()
}

// ── WebSocket chat ─────────────────────────────────────────────────────────────

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	userName := r.URL.Query().Get("user")
	if userName == "" {
		http.Error(w, "missing ?user=", http.StatusBadRequest)
		return
	}

	userCfg := s.cfg.GetUser(userName)
	if userCfg == nil {
		http.Error(w, "unknown user", http.StatusForbidden)
		return
	}

	// Resolve the role/age override (if any) that supersedes the config row.
	adjustedUser := s.resolveUserRole(r.Context(), userName)
	// Initialize last known role and ageGroup from the adjustedUser for change detection.
	lastRole := adjustedUser.Role
	lastAgeGroup := adjustedUser.AgeGroup

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] upgrade error: %v", err)
		return
	}
	defer conn.Close()

	s.clientsMu.Lock()
	s.clients[conn] = userName
	s.clientsMu.Unlock()
	defer func() {
		s.clientsMu.Lock()
		delete(s.clients, conn)
		s.clientsMu.Unlock()
	}()

	log.Printf("[ws] %s connected", userCfg.DisplayName)

	var llmClient llm.Chatter
	switch s.cfg.LLM.Provider {
	case "claude_cli":
		llmClient = claudecli.New()
	default:
		ep := s.cfg.LLMEndpointFor(userCfg)
		llmClient = llm.NewClient(ep.BaseURL, ep.Model, ep.APIKey)
	}
	a, err := agent.NewAgent(adjustedUser, s.cfg, llmClient, s.evaluator, s.clf, s.db, agent.AgentDeps{
		Skills: s.skills,
		Pool:   s.pool,
		MsgContext: gateway.MsgContext{Gateway: "web", ExternalID: adjustedUser.Name},
	})
	if err != nil {
		log.Printf("[ws] failed to create agent for %s: %v", userCfg.DisplayName, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	for {
		var msg WsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err) {
				log.Printf("[ws] %s disconnected", userCfg.DisplayName)
			}
			return
		}

		// Check for role override changes and recreate agent if needed.
		currentRole := userCfg.Role
		currentAgeGroup := userCfg.AgeGroup
		if role, ageGroup, err := s.db.GetRoleOverride(r.Context(), userName); err == nil {
			if role != "" {
				currentRole = role
			}
			if ageGroup != "" {
				currentAgeGroup = ageGroup
			}
		}
		if currentRole != lastRole || currentAgeGroup != lastAgeGroup {
			// Recreate agent with the new role/ageGroup.
			// Copy the entire user config to preserve fields like Color, Model, LLMProfile.
			copied := *userCfg
			copied.Role = currentRole
			copied.AgeGroup = currentAgeGroup
			adjustedUser := &copied
			a, err = agent.NewAgent(adjustedUser, s.cfg, llmClient, s.evaluator, s.clf, s.db, agent.AgentDeps{
				Skills: s.skills,
				Pool:   s.pool,
				MsgContext: gateway.MsgContext{Gateway: "web", ExternalID: adjustedUser.Name},
			})
			lastRole = currentRole
			if err != nil {
				log.Printf("[ws] failed to recreate agent for %s: %v", userCfg.DisplayName, err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			lastAgeGroup = currentAgeGroup
		}

		switch msg.Type {
		case "chat":
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(msg.Payload, &payload); err != nil || payload.Text == "" {
				continue
			}

			// Send "typing" indicator
			s.sendWS(conn, "typing", map[string]bool{"typing": true})

			// Stream tokens back to client
			var full strings.Builder
			onToken := func(token string) {
				s.sendWS(conn, "token", map[string]string{"token": token})
				full.WriteString(token)
			}

			chatCtx, chatCancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer chatCancel()
			resp, err := a.Chat(chatCtx, payload.Text, onToken)
			if err != nil {
				errMsg := err.Error()
				if chatCtx.Err() != nil {
					errMsg = "The AI took too long to respond. Check that your AI service is running, or try a different one in Settings."
				}
				s.sendWS(conn, "error", map[string]string{"error": errMsg})
				continue
			}

			// If it was a policy block, we didn't stream — send full message now
			if resp.PolicyAction != "allow" {
				s.sendWS(conn, "message", map[string]any{
					"role":          "assistant",
					"content":       resp.Content,
					"policy_action": resp.PolicyAction,
					"category":      resp.Category,
				})
			} else {
				// Signal end of stream
				s.sendWS(conn, "done", map[string]any{
					"policy_action": resp.PolicyAction,
					"category":      resp.Category,
				})
			}

			// Notify parent if approval needed
			if resp.PolicyAction == "request_approval" {
				go s.requestApproval(userCfg, string(resp.Category), payload.Text)
			}

			// Broadcast dashboard update
			go s.broadcastDashboardUpdate(context.Background())

		case "ping":
			s.sendWS(conn, "pong", nil)
		}
	}
}

func (s *Server) sendWS(conn *websocket.Conn, msgType string, payload any) {
	var raw json.RawMessage
	if payload != nil {
		b, _ := json.Marshal(payload)
		raw = b
	}
	conn.WriteJSON(WsMessage{Type: msgType, Payload: raw}) //nolint:errcheck
}

// ── REST API ──────────────────────────────────────────────────────────────────

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	type userView struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
		AgeGroup    string `json:"age_group,omitempty"`
		Color       string `json:"color"`
	}
	var users []userView
	for _, u := range s.cfg.Users {
		users = append(users, userView{
			Name:        u.Name,
			DisplayName: u.DisplayName,
			Role:        u.Role,
			AgeGroup:    u.AgeGroup,
			Color:       u.Color,
		})
	}
	jsonOK(w, users)
}

func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		scope := r.URL.Query().Get("scope")
		var approvals []*store.Approval
		var err error
		if scope == "all" {
			approvals, err = s.db.RecentApprovals(100)
		} else {
			approvals, err = s.db.PendingApprovals(r.Context())
		}
		if err != nil {
			jsonErr(w, err, http.StatusInternalServerError)
			return
		}
		jsonOK(w, approvals)
	}
}

func (s *Server) handleDecide(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	// Auth gate: this route is mounted via s.protect(...) — the session
	// middleware has already validated the cookie before we get here.

	var body struct {
		ID     string `json:"id"`
		Action string `json:"action"` // approve | deny
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, err, http.StatusBadRequest)
		return
	}

	status := "approved"
	if body.Action == "deny" {
		status = "denied"
	}

	if err := s.db.DecideApproval(body.ID, status, "parent"); err != nil {
		jsonErr(w, err, http.StatusBadRequest)
		return
	}

	go s.broadcastDashboardUpdate(context.Background())
	jsonOK(w, map[string]string{"status": status})
}

// handleDecideLink handles one-click approve/deny links from emails/SMS.
func (s *Server) handleDecideLink(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	action := r.URL.Query().Get("action")
	token := r.URL.Query().Get("token")

	if token == "" {
		http.Error(w, "missing parameters", http.StatusBadRequest)
		return
	}

	// Verify token — checks HMAC signature AND expiry from embedded timestamp
	tokenID, tokenAction, err := notify.VerifyToken(token, s.cfg.Server.Secret, s.cfg.Approval.ExpiryHours)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid or expired link: %v", err), http.StatusForbidden)
		return
	}

	// Use values from token, not URL params (prevents tampering)
	id = tokenID
	action = tokenAction

	// Replay protection — each token can only be used once
	tokenHash := sha256Hex(token)
	isNew, markErr := s.db.MarkTokenUsed(tokenHash)
	if markErr != nil {
		http.Error(w, "Internal error processing approval", http.StatusInternalServerError)
		log.Printf("[web] token replay check error: %v", markErr)
		return
	}
	if !isNew {
		http.Error(w, "This approval link has already been used.", http.StatusConflict)
		return
	}

	status := "approved"
	if action == "deny" {
		status = "denied"
	}
	if err := s.db.DecideApproval(id, status, "parent-link"); err != nil {
		log.Printf("[web] decide approval error: %v", err)
		http.Error(w, "Internal server error", http.StatusBadRequest)
		return
	}

	go s.broadcastDashboardUpdate(context.Background())

	icon := "✅"
	if status == "denied" {
		icon = "❌"
	}
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>FamClaw</title>
<meta http-equiv="refresh" content="3;url=http://%s.local:%d">
<style>body{font-family:system-ui;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;background:#f8fafc}
.card{background:#fff;border-radius:16px;padding:40px;text-align:center;box-shadow:0 4px 24px rgba(0,0,0,.08);max-width:360px}
span{font-size:56px;display:block;margin-bottom:16px}
h2{margin:0 0 8px;font-size:22px}p{color:#6b7280;margin:0}</style></head>
<body><div class="card"><span>%s</span>
<h2>Request %s</h2>
<p>Redirecting to dashboard…</p>
</div></body></html>`,
		s.cfg.Server.MDNSName, s.cfg.Server.Port, icon, status)
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	if s.skillRegistry == nil {
		jsonOK(w, []*skillbridge.Skill{})
		return
	}
	skills, err := s.skillRegistry.List()
	if err != nil {
		jsonErr(w, err, http.StatusInternalServerError)
		return
	}
	jsonOK(w, skills)
}
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	var names []string
	for name := range s.cfg.Skills.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	jsonOK(w, map[string][]string{"servers": names})
}

func (s *Server) handleMCPAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name string            `json:"name"`
		Config config.MCPServerConfig `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, fmt.Errorf("decoding body: %w", err), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		jsonErr(w, fmt.Errorf("name is required"), http.StatusBadRequest)
		return
	}
	if err := config.ValidateMCPServer(name, body.Config); err != nil {
		jsonErr(w, err, http.StatusBadRequest)
		return
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if s.cfg.Skills.MCPServers == nil {
		s.cfg.Skills.MCPServers = make(map[string]config.MCPServerConfig)
	}
	s.cfg.Skills.MCPServers[name] = body.Config
	if err := s.cfg.Save(s.cfgPath); err != nil {
		jsonErr(w, fmt.Errorf("saving config: %w", err), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"status": "saved"})
}

func (s *Server) handleMCPRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, fmt.Errorf("decoding body: %w", err), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		jsonErr(w, fmt.Errorf("name is required"), http.StatusBadRequest)
		return
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if s.cfg.Skills.MCPServers != nil {
		delete(s.cfg.Skills.MCPServers, name)
	}
	if err := s.cfg.Save(s.cfgPath); err != nil {
		jsonErr(w, fmt.Errorf("saving config: %w", err), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"status": "removed", "name": name})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"status":      "ok",
		"version":     "1.0.0",
		"time":        time.Now().UTC(),
		"needs_setup": s.NeedsSetup(),
	})
}

// ── Server-Sent Events for dashboard live updates ────────────────────────────

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			pending, _ := s.db.PendingApprovals(r.Context())
			data, _ := json.Marshal(map[string]any{"pending_count": len(pending)})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Server) requestApproval(user *config.UserConfig, category, queryText string) {
	import_approval_id := agent.ApprovalID(user.Name, category)
	a := &store.Approval{
		ID:          import_approval_id,
		UserName:    user.Name,
		UserDisplay: user.DisplayName,
		AgeGroup:    user.AgeGroup,
		Category:    category,
		QueryText:   queryText,
	}
	isNew, err := s.db.UpsertApproval(a)
	if err != nil {
		log.Printf("[web] approval upsert: %v", err)
		return
	}
	if isNew && s.notifier != nil {
		approveURL := fmt.Sprintf("http://%s.local:%d/decide?id=%s&action=approve&token=%s",
			s.cfg.Server.MDNSName, s.cfg.Server.Port, a.ID,
			notify.GenerateToken(a.ID, "approve", s.cfg.Server.Secret))
		denyURL := fmt.Sprintf("http://%s.local:%d/decide?id=%s&action=deny&token=%s",
			s.cfg.Server.MDNSName, s.cfg.Server.Port, a.ID,
			notify.GenerateToken(a.ID, "deny", s.cfg.Server.Secret))
		s.notifier.Notify(context.Background(), a, approveURL, denyURL)
	}
}

func (s *Server) broadcastDashboardUpdate(ctx context.Context) {
	if s.db == nil {
		return
	}
	pending, err := s.db.PendingApprovals(ctx)
	if err != nil {
		log.Printf("[web] dashboard broadcast pending approvals: %v", err)
		return
	}
	var unknown any
	if s.identStore != nil {
		u, err := s.identStore.ListUnknown(ctx)
		if err != nil {
			log.Printf("[web] dashboard broadcast list unknown: %v", err)
			return
		} else {
			unknown = u
		}
	}
	var installedSkills []*skillbridge.Skill
	if s.skillRegistry != nil {
		installedSkills, _ = s.skillRegistry.List()
	}
	payload, _ := json.Marshal(map[string]any{
		"pending_count":    len(pending),
		"unknown_accounts": unknown,
		"installed_skills": installedSkills,
	})

	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	for conn, userName := range s.clients {
		_ = userName
		conn.WriteJSON(WsMessage{Type: "dashboard_update", Payload: payload}) //nolint:errcheck
	}
}

// handleConversations returns recent messages for a user (parent dashboard).
// Mounted behind s.protect(...); the session middleware enforces auth.
func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	userName := r.URL.Query().Get("user")
	if userName == "" {
		jsonErr(w, fmt.Errorf("missing ?user= parameter"), http.StatusBadRequest)
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	msgs, err := s.db.RecentMessagesByUser(userName, limit)
	if err != nil {
		jsonErr(w, err, http.StatusInternalServerError)
		return
	}

	jsonOK(w, msgs)
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func jsonErr(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
}

// resolveUserRole returns the effective UserConfig for userName, applying any
// DB-persisted role/age override (set via set_user_role) that supersedes the
// config row. Returns userCfg unchanged when no override exists.
func (s *Server) resolveUserRole(ctx context.Context, userName string) *config.UserConfig {
	userCfg := s.cfg.GetUser(userName)
	if userCfg == nil {
		return nil
	}
	role, ageGroup, err := s.db.GetRoleOverride(ctx, userName)
	if err != nil {
		log.Printf("[ws] %s: GetRoleOverride error: %v — falling back to config", userName, err)
		return userCfg
	}
	if role != "" || ageGroup != "" {
		copied := *userCfg
		if role != "" {
			copied.Role = role
		}
		if ageGroup != "" {
			copied.AgeGroup = ageGroup
		}
		return &copied
	}
	return userCfg
}
