package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/famclaw/famclaw/internal/hardware"
)

// handleSetupDetect returns hardware capabilities for the setup wizard.
// Only available during first boot (no parent with PIN configured).
func (s *Server) handleSetupDetect(w http.ResponseWriter, r *http.Request) {
	if !s.isFirstBoot() {
		http.Error(w, "setup already complete", http.StatusForbidden)
		return
	}
	info := hardware.Detect()
	jsonOK(w, info)
}

// handleTestTelegram verifies a Telegram bot token by calling the platform's
// getMe endpoint. Returns {ok: true, username: "..."} on success or
// {ok: false, error: "<reason>"} on failure. Always replies HTTP 200 — the
// wizard treats `ok` as the rejection signal, not the HTTP status.
//
// NOT behind the parent-PIN gate: the wizard reaches this on first boot
// before any PIN exists. The endpoint only forwards to a fixed upstream
// URL and returns ok/error, so the LAN-exposed surface is minimal.
func (s *Server) handleTestTelegram(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid JSON"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://api.telegram.org/bot%s/getMe", body.Token)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "network error"})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid token"})
		return
	}

	var tg struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tg); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "decode error"})
		return
	}
	if !tg.OK {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid token"})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"username": tg.Result.Username,
	})
}

// handleTestDiscord verifies a Discord bot token by calling /users/@me.
// The bot's user-id doubles as the application id used in the OAuth2
// invite URL the wizard generates client-side.
func (s *Server) handleTestDiscord(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid JSON"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://discord.com/api/v10/users/@me", nil)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	req.Header.Set("Authorization", "Bot "+body.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "network error"})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid token"})
		return
	}

	var dc struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "decode error"})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"username": dc.Username,
		"app_id":   dc.ID,
	})
}

// handleRoot serves the app. Redirects to /setup if unconfigured.
// /setup serves index.html (wizard is triggered by JS based on needs_setup).
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	// Redirect root to /setup if unconfigured
	if r.URL.Path == "/" && s.NeedsSetup() {
		http.Redirect(w, r, "/setup", http.StatusTemporaryRedirect)
		return
	}
	// /setup serves the same index.html — wizard is a JS-driven screen
	if r.URL.Path == "/setup" {
		r.URL.Path = "/"
		s.staticHandler.ServeHTTP(w, r)
		return
	}
	// Everything else: normal static files
	s.staticHandler.ServeHTTP(w, r)
}
