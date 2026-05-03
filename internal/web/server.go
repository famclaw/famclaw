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
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/famclaw/famclaw/internal/agent"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/skillbridge"
	"github.com/famclaw/famclaw/internal/store"
)

//go:embed static
var staticFiles embed.FS

// Server is the FamClaw web server.
type Server struct {
	cfg        *config.Config
	cfgPath    string // path to config.yaml for settings API
	db         *store.DB
	evaluator  *policy.Evaluator
	clf        *classifier.Classifier
	notifier   *notify.MultiNotifier
	skills        []*skillbridge.Skill  // injected into agent system prompt
	skillRegistry *skillbridge.Registry // backs POST /api/skills/install + /remove
	pool          *mcp.Pool             // MCP tool pool for agent tool calls
	oauthStore    *llm.OAuthStore     // OAuth token store for subscription auth
	oauthFlow     *llm.OAuthFlow      // active OAuth flow (nil when not in progress)
	staticHandler http.Handler        // embedded static file server
	upgrader      websocket.Upgrader
	cfgMu      sync.RWMutex               // guards cfg during settings reads/writes
	clients    map[*websocket.Conn]string // conn → userName
	clientsMu  sync.RWMutex
}

// wsMessage is a WebSocket protocol message.
type wsMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func NewServer(cfg *config.Config, cfgPath string, db *store.DB, evaluator *policy.Evaluator,
	clf *classifier.Classifier, notifier *notify.MultiNotifier, skills []*skillbridge.Skill, skillRegistry *skillbridge.Registry, pool *mcp.Pool, oauthStore *llm.OAuthStore) *Server {
	return &Server{
		cfg:           cfg,
		cfgPath:       cfgPath,
		db:            db,
		evaluator:     evaluator,
		clf:           clf,
		notifier:      notifier,
		pool:          pool,
		oauthStore:    oauthStore,
		skills:        skills,
		skillRegistry: skillRegistry,
		clients:       make(map[*websocket.Conn]string),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				// Allow connections from LAN — all origins on local network
				return true
			},
		},
	}
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ── Static files (embedded web UI) ────────────────────────────────────────
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("static files: %v", err)
	}
	s.staticHandler = http.FileServer(http.FS(staticFS))
	// Root: redirect to /setup if unconfigured, otherwise serve static files
	mux.HandleFunc("/", s.handleRoot)

	// ── API ───────────────────────────────────────────────────────────────────
	mux.HandleFunc("/api/users", s.handleUsers)
	mux.HandleFunc("/api/chat", s.handleChat)              // WebSocket
	mux.HandleFunc("/api/approvals", s.handleApprovals)
	mux.HandleFunc("/api/approvals/decide", s.handleDecide)
	mux.HandleFunc("/api/skills", s.handleSkills)
	mux.HandleFunc("/api/skills/install", s.handleSkillInstall)
	mux.HandleFunc("/api/skills/remove", s.handleSkillRemove)
	mux.HandleFunc("/api/conversations", s.handleConversations)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/setup/detect", s.handleSetupDetect)
	mux.HandleFunc("/api/setup/test-telegram", s.handleTestTelegram)
	mux.HandleFunc("/api/setup/test-discord", s.handleTestDiscord)
	mux.HandleFunc("/api/settings", s.handleSettings)      // GET/POST config settings
	mux.HandleFunc("/api/stream", s.handleStream)          // SSE for dashboard live updates
	mux.HandleFunc("/api/oauth/anthropic/start", s.handleOAuthStart)
	mux.HandleFunc("/api/oauth/anthropic/callback", s.handleOAuthCallback)
	mux.HandleFunc("/api/oauth/anthropic/status", s.handleOAuthStatus)

	// ── Parent decision links (from email/SMS) ────────────────────────────────
	mux.HandleFunc("/decide", s.handleDecideLink)

	return mux
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

	ep := s.cfg.LLMEndpointFor(userCfg)
	var llmClient *llm.Client
	if ep.AuthType == "oauth" && s.oauthStore != nil {
		llmClient = llm.NewOAuthClient(ep.BaseURL, ep.Model, s.oauthStore, "anthropic")
	} else {
		llmClient = llm.NewClient(ep.BaseURL, ep.Model, ep.APIKey)
	}
	a := agent.NewAgent(userCfg, s.cfg, llmClient, s.evaluator, s.clf, s.db, agent.AgentDeps{
		Skills:     s.skills,
		Pool:       s.pool,
		OAuthStore: s.oauthStore,
	})

	for {
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err) {
				log.Printf("[ws] %s disconnected", userCfg.DisplayName)
			}
			return
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
			go s.broadcastDashboardUpdate()

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
	conn.WriteJSON(wsMessage{Type: msgType, Payload: raw}) //nolint:errcheck
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
			approvals, err = s.db.PendingApprovals()
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

	// Require parent PIN for dashboard decisions
	pin := r.Header.Get("X-Parent-PIN")
	if !s.verifyParentPIN(pin) {
		jsonErr(w, fmt.Errorf("invalid PIN"), http.StatusForbidden)
		return
	}

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

	go s.broadcastDashboardUpdate()
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
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	go s.broadcastDashboardUpdate()

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
	skills, err := s.db.ListSkills()
	if err != nil {
		jsonErr(w, err, http.StatusInternalServerError)
		return
	}
	jsonOK(w, skills)
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
			pending, _ := s.db.PendingApprovals()
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

func (s *Server) broadcastDashboardUpdate() {
	if s.db == nil {
		return
	}
	pending, _ := s.db.PendingApprovals()
	var installedSkills []*skillbridge.Skill
	if s.skillRegistry != nil {
		installedSkills, _ = s.skillRegistry.List()
	}
	payload, _ := json.Marshal(map[string]any{
		"pending_count":    len(pending),
		"installed_skills": installedSkills,
	})

	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	for conn, userName := range s.clients {
		_ = userName
		conn.WriteJSON(wsMessage{Type: "dashboard_update", Payload: payload}) //nolint:errcheck
	}
}

func (s *Server) verifyParentPIN(pin string) bool {
	for _, u := range s.cfg.Users {
		if u.Role == "parent" && u.PIN == pin {
			return true
		}
	}
	return false
}

// handleConversations returns recent messages for a user (parent dashboard).
// Requires parent PIN.
func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	pin := r.Header.Get("X-Parent-PIN")
	if !s.verifyParentPIN(pin) {
		jsonErr(w, fmt.Errorf("parent PIN required"), http.StatusForbidden)
		return
	}

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
