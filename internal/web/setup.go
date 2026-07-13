package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/famclaw/famclaw/internal/hardware"
	"github.com/famclaw/famclaw/internal/notify"
)

// handleSetupDetect returns hardware capabilities for the setup wizard.
// Only available during first boot — defined here as "no parent_pin row in
// the vault yet". Once the PIN is bootstrapped (Phase 7), this endpoint stops
// answering so a LAN attacker cannot probe the host's hardware after setup.
func (s *Server) handleSetupDetect(w http.ResponseWriter, r *http.Request) {
	if s.hasPINConfigured(r.Context()) {
		http.Error(w, "setup already complete", http.StatusForbidden)
		return
	}
	info := hardware.Detect()
	jsonOK(w, info)
}

// handleTestTelegram verifies a Telegram bot token by calling the platform's
// getMe endpoint. Returns {ok: true, username: "..."} on success or
// {ok: false, error: "<reason>"} on upstream failure. Always replies HTTP 200
// for upstream errors so the wizard can use `ok` as the rejection signal.
//
// Mounted behind s.protect(...): the session middleware enforces auth before
// this handler runs. First-boot bypass is handled by Phase 7's PIN-bootstrap
// flow seating a session cookie before the wizard reaches gateway probing.
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
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": notify.RedactWebhookURLInError(err).Error()})
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": notify.RedactWebhookURLInError(err).Error()})
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
// The bot's user-id doubles as the application id used in the bot invite
// URL the wizard generates client-side. Mounted behind s.protect(...) — see
// handleTestTelegram for the auth rationale.
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
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": notify.RedactWebhookURLInError(err).Error()})
		return
	}
	req.Header.Set("Authorization", "Bot "+body.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": notify.RedactWebhookURLInError(err).Error()})
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
//
// When the vault is in machine-mismatch state every non-essential request is
// funnelled to /unlock. /unlock itself, its POST endpoint, and the health
// probe are exempt so the rebind UI can load and the boot loop can still ask
// "are you alive". Everything else — including static assets — short-circuits
// to a redirect, which is intentional: serving the regular dashboard while
// the vault is in mismatch state would invite users to start typing their
// PIN into a UI that cannot decrypt anything yet.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if s.getVaultMismatch() {
		switch r.URL.Path {
		case "/unlock":
			r.URL.Path = "/unlock.html"
			s.staticHandler.ServeHTTP(w, r)
			return
		case "/api/setup/unlock", "/api/health":
			// Allow the rebind endpoint and health probe through to their own
			// handlers. We must NOT redirect — handleRoot is on "/", so these
			// paths only land here if their dedicated routes are not yet
			// matched (i.e. never in normal routing). Falling through keeps
			// behaviour safe regardless of mux ordering.
		default:
			http.Redirect(w, r, "/unlock", http.StatusFound)
			return
		}
	}
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
	// /login serves the dedicated PIN entry page from the embedded static tree.
	if r.URL.Path == "/login" {
		r.URL.Path = "/login.html"
		s.staticHandler.ServeHTTP(w, r)
		return
	}
	// /unlock served outside of mismatch state still returns the page so an
	// operator can preview it; the POST handler enforces the real gate.
	if r.URL.Path == "/unlock" {
		r.URL.Path = "/unlock.html"
		s.staticHandler.ServeHTTP(w, r)
		return
	}
	// Everything else: normal static files
	s.staticHandler.ServeHTTP(w, r)
}

// handleSetupPIN bootstraps the parent PIN on first boot. Idempotent on the
// happy path: the INSERT uses ON CONFLICT DO NOTHING, and the precheck via
// hasPINConfigured returns 409 before we even hash. The two checks together
// close the small race where two browser tabs both think they're the first.
//
// On success the handler seats a session cookie immediately so the wizard
// can keep going without bouncing through /login — first boot is the one
// time we know the user just demonstrated knowledge of the PIN they set.
func (s *Server) handleSetupPIN(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	if s.hasPINConfigured(ctx) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "pin already set"})
		return
	}

	var req struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.PIN) < 4 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pin too short"})
		return
	}

	pinHash := sha256.Sum256([]byte(req.PIN))
	ct, err := s.vault.Encrypt(pinHash[:])
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	res, err := s.db.SQL().ExecContext(ctx,
		`INSERT INTO vault_secrets(name, ciphertext, updated_at) VALUES('parent_pin', ?, ?) ON CONFLICT(name) DO NOTHING`,
		ct, time.Now().Unix())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	rows, err := res.RowsAffected()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if rows == 0 {
		// Lost the race against a concurrent first-boot request.
		writeJSON(w, http.StatusConflict, map[string]string{"error": "pin already set"})
		return
	}

	userID, err := s.resolveParentUserID(ctx)
	if err != nil {
		// First-boot fallback: a freshly-shipped config.yaml may not yet have a
		// declared parent. The session row's user_id is informational only, so
		// pinning it to 1 keeps accounting honest until the wizard writes a
		// real parent into the YAML.
		log.Printf("[setup] resolveParentUserID failed during PIN bootstrap, using fallback userID=1: %v", err)
		userID = 1
	}

	sid, err := s.sessions.Create(ctx, userID, clientIP(r), r.Header.Get("User-Agent"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "famclaw_session",
		Value:    sid,
		Path:     "/",
		MaxAge:   7 * 24 * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":  userID,
		"redirect": "/dashboard",
	})
}

// handleSetupUnlock re-encrypts the parent PIN under the current machine ID
// after a hardware-fingerprint change. It does NOT verify the submitted PIN
// against the old ciphertext — by definition the old HKDF key is unreachable
// once the machine ID has changed, so the brute-force defence is the shared
// /login rate limiter on s.auth, not cryptographic verification.
//
// The endpoint is invisible (404) when the vault is healthy. That keeps
// network probes from learning whether a host has ever entered mismatch
// state, which would otherwise leak the existence of a recovery surface.
func (s *Server) handleSetupUnlock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.getVaultMismatch() {
		// Hide the endpoint when no mismatch is in progress — leaking its
		// existence would tell a probe "this host has a recovery surface".
		http.NotFound(w, r)
		return
	}
	if s.auth == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	// minLatency floor must wrap every response branch below — otherwise an
	// attacker could distinguish "rate-limited" (instant) from "wrong PIN"
	// (computes vault round-trip) and sidestep the limiter by changing IPs
	// once they've measured the gap.
	start := time.Now()
	defer func() {
		if d := s.auth.minLatency - time.Since(start); d > 0 {
			time.Sleep(d)
		}
	}()

	limiter := s.auth.Limiter()
	ip := clientIP(r)
	allowed, retryAfter := limiter.check(ip)
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts"})
		return
	}

	// Trust boundary outside the machine-bound vault: require a pre-existing
	// authenticated session before re-keying. web_sessions rows are not
	// encrypted under the machine-derived key, so a session created before the
	// machine-id change is still valid here — i.e. the caller already proved
	// knowledge of the prior PIN. Without this check, anyone reaching this
	// endpoint during mismatch state could submit any PIN and take over admin.
	if s.sessions == nil {
		limiter.recordFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	cookie, cerr := r.Cookie("famclaw_session")
	if cerr != nil || cookie.Value == "" {
		limiter.recordFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	if _, serr := s.sessions.Get(r.Context(), cookie.Value); serr != nil {
		limiter.recordFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	var req struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.PIN) < 4 {
		limiter.recordFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	pinHash := sha256.Sum256([]byte(req.PIN))
	ct, err := s.vault.Encrypt(pinHash[:])
	if err != nil {
		limiter.recordFailure(ip)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	// Round-trip sanity check: if Encrypt succeeded but Decrypt cannot read it
	// back we'd be writing an unrecoverable blob into the vault and locking
	// the user out at the next login. Bail before touching the DB.
	if _, err := s.vault.Decrypt(ct); err != nil {
		limiter.recordFailure(ip)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	ctx := r.Context()
	if _, err := s.db.SQL().ExecContext(ctx,
		`UPDATE vault_secrets SET ciphertext=?, updated_at=? WHERE name='parent_pin'`,
		ct, time.Now().Unix()); err != nil {
		limiter.recordFailure(ip)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	s.vaultMu.Lock()
	s.vaultMismatch = false
	s.vaultMu.Unlock()

	log.Printf("[SECURITY] vault re-keyed after machine-id change at %s; if you did not authorise this, rotate PIN immediately",
		time.Now().UTC().Format(time.RFC3339))

	limiter.recordSuccess(ip)

	userID, err := s.resolveParentUserID(ctx)
	if err != nil {
		log.Printf("[setup] resolveParentUserID failed during unlock, using fallback userID=1: %v", err)
		userID = 1
	}

	sid, err := s.sessions.Create(ctx, userID, ip, r.Header.Get("User-Agent"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "famclaw_session",
		Value:    sid,
		Path:     "/",
		MaxAge:   7 * 24 * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":  userID,
		"redirect": "/dashboard",
	})
}
