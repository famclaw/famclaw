// Package web provides the FamClaw HTTP server.
// Serves the embedded web UI and a REST+WebSocket API.
// All local — no CDN dependencies, works offline on RPi/old phones.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/famclaw/famclaw/internal/agent"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/notify"
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
	upgrader   websocket.Upgrader
	clients    map[*websocket.Conn]string // conn → userName
	clientsMu  sync.RWMutex
}

// wsMessage is a WebSocket protocol message.
type wsMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func NewServer(cfg *config.Config, cfgPath string, db *store.DB, evaluator *policy.Evaluator,
	clf *classifier.Classifier, notifier *notify.MultiNotifier) *Server {
	return &Server{
		cfg:       cfg,
		cfgPath:   cfgPath,
		db:        db,
		evaluator: evaluator,
		clf:       clf,
		notifier:  notifier,
		clients:   make(map[*websocket.Conn]string),
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
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	// ── API ───────────────────────────────────────────────────────────────────
	mux.HandleFunc("/api/users", s.handleUsers)
	mux.HandleFunc("/api/chat", s.handleChat)              // WebSocket
	mux.HandleFunc("/api/approvals", s.handleApprovals)
	mux.HandleFunc("/api/approvals/decide", s.handleDecide)
	mux.HandleFunc("/api/skills", s.handleSkills)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/settings", s.handleSettings)      // GET/POST config settings
	mux.HandleFunc("/api/stream", s.handleStream)          // SSE for dashboard live updates

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

	llmClient := llm.NewClient(s.cfg.LLM.BaseURL, s.cfg.ModelFor(userCfg), s.cfg.LLM.APIKey)
	a := agent.NewAgent(userCfg, s.cfg, llmClient, s.evaluator, s.clf, s.db)

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

			ctx := r.Context()
			resp, err := a.Chat(ctx, payload.Text, onToken)
			if err != nil {
				s.sendWS(conn, "error", map[string]string{"error": err.Error()})
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

	if id == "" || action == "" || token == "" {
		http.Error(w, "missing parameters", http.StatusBadRequest)
		return
	}

	// Verify HMAC token
	expected := notify.GenerateToken(id, action, s.cfg.Server.Secret)
	if token != expected {
		http.Error(w, "invalid token", http.StatusForbidden)
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
	pending, _ := s.db.PendingApprovals()
	payload, _ := json.Marshal(map[string]any{"pending_count": len(pending)})

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

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func jsonErr(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
}
