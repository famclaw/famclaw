package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/famclaw/famclaw/internal/credstore"
	"github.com/famclaw/famclaw/internal/store"
)

// Rate-limit constants for /login. The limiter tracks attempts per source IP:
// after maxAttempts failures within window, the IP is locked for lockout.
const (
	maxAttempts = 5
	window      = 15 * time.Minute
	lockout     = 1 * time.Minute
)

// rateLimitEntry is the per-IP state tracked by loginLimiter.
type rateLimitEntry struct {
	attempts    int
	windowStart time.Time
	lockedUntil time.Time
}

// loginLimiter is an in-memory per-IP rate limiter for /login. The `now`
// function is injected so tests can advance the clock deterministically; in
// production it is time.Now.
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]*rateLimitEntry
	now     func() time.Time
}

// newLoginLimiter constructs an empty loginLimiter with time.Now as the clock.
func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		entries: make(map[string]*rateLimitEntry),
		now:     time.Now,
	}
}

// check reports whether the given IP is currently allowed to attempt a login.
// When locked out, retryAfter is the remaining lockout duration; otherwise 0.
// check does NOT increment the attempt counter — only recordFailure does.
func (l *loginLimiter) check(ip string) (allowed bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.entries[ip]
	if !ok {
		e = &rateLimitEntry{windowStart: now}
		l.entries[ip] = e
	}
	if now.Before(e.lockedUntil) {
		return false, e.lockedUntil.Sub(now)
	}
	if now.Sub(e.windowStart) > window {
		e.attempts = 0
		e.windowStart = now
	}
	return true, 0
}

// recordFailure increments the attempt counter for ip. When the count reaches
// maxAttempts the IP is locked for `lockout` duration and the window is reset
// to start after the lockout expires.
func (l *loginLimiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.entries[ip]
	if !ok {
		e = &rateLimitEntry{windowStart: now}
		l.entries[ip] = e
	}
	e.attempts++
	if e.attempts >= maxAttempts {
		e.lockedUntil = now.Add(lockout)
		e.attempts = 0
		e.windowStart = now.Add(lockout)
	}
}

// recordSuccess clears the per-IP state on successful login.
func (l *loginLimiter) recordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, ip)
}

// AuthHandler serves /login, /logout and /session. Dependencies are injected
// to keep the type unit-testable without spinning up a full *Server.
//
// minLatency enforces a constant-time floor on /login so attackers cannot
// distinguish "wrong PIN" from "no PIN configured" by response timing.
type AuthHandler struct {
	sessions         *store.SessionStore
	vault            *credstore.Vault
	getPINCiphertext func(context.Context) ([]byte, error)
	resolveUserID    func(context.Context) (int64, error)
	limiter          *loginLimiter
	minLatency       time.Duration
}

// Limiter exposes the per-IP rate limiter so other handlers (notably the
// machine-mismatch unlock flow in /api/setup/unlock) can share the same
// budget as /login. Sharing the limiter is deliberate: from an attacker's
// perspective both endpoints unlock the same vault secret, so a brute-force
// attempt against either should consume the same per-IP attempt counter.
func (a *AuthHandler) Limiter() *loginLimiter { return a.limiter }

// NewAuthHandler constructs an AuthHandler with the default 250ms latency
// floor and a fresh in-memory rate limiter.
func NewAuthHandler(
	sessions *store.SessionStore,
	vault *credstore.Vault,
	getPIN func(context.Context) ([]byte, error),
	resolveUserID func(context.Context) (int64, error),
) *AuthHandler {
	return &AuthHandler{
		sessions:         sessions,
		vault:            vault,
		getPINCiphertext: getPIN,
		resolveUserID:    resolveUserID,
		limiter:          newLoginLimiter(),
		minLatency:       250 * time.Millisecond,
	}
}

// HandleLogin verifies the submitted PIN against the encrypted hash stored in
// the vault and, on success, issues a session cookie. All failure paths return
// the SAME 401 body — wrong PIN, no PIN configured, vault decrypt failure are
// indistinguishable to the caller. The handler enforces a minLatency floor on
// every code path so attackers cannot infer state from response timing.
func (a *AuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		http.Redirect(w, r, "/login.html", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	defer func() {
		if d := a.minLatency - time.Since(start); d > 0 {
			time.Sleep(d)
		}
	}()

	ip := clientIP(r)
	allowed, retryAfter := a.limiter.check(ip)
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts"})
		return
	}

	var req struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		a.limiter.recordFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	ct, err := a.getPINCiphertext(r.Context())
	if err != nil {
		// sql.ErrNoRows (no PIN configured) is intentionally surfaced as the
		// same generic 401 — see HARD CONSTRAINT in the phase spec.
		a.limiter.recordFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	pinBytes, err := a.vault.Decrypt(ct)
	if err != nil {
		// /login NEVER offers re-encrypt on machine mismatch — that affordance
		// belongs to /api/setup/unlock only. Treat any decrypt failure as a
		// generic 401 to preserve the constant response shape.
		a.limiter.recordFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	pinHash := sha256.Sum256([]byte(req.PIN))
	if subtle.ConstantTimeCompare(pinHash[:], pinBytes) != 1 {
		a.limiter.recordFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	a.limiter.recordSuccess(ip)

	userID, err := a.resolveUserID(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	sid, err := a.sessions.Create(r.Context(), userID, ip, r.Header.Get("User-Agent"))
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
	writeJSON(w, http.StatusOK, map[string]int64{"user_id": userID})
}

// HandleLogout deletes the server-side session row (best-effort) and clears
// the cookie. Returns 204 unconditionally — logging out an already-expired or
// missing session is not an error from the caller's perspective.
func (a *AuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if cookie, err := r.Cookie("famclaw_session"); err == nil {
		_ = a.sessions.Delete(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "famclaw_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	w.WriteHeader(http.StatusNoContent)
}

// HandleSession reports whether the requestor holds a valid session cookie.
// Missing/expired/invalid cookies all return {"logged_in": false} with 200 —
// the absence of a session is not an error.
func (a *AuthHandler) HandleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	cookie, err := r.Cookie("famclaw_session")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"logged_in": false})
		return
	}
	sess, err := a.sessions.Get(r.Context(), cookie.Value)
	if errors.Is(err, store.ErrNoSession) {
		writeJSON(w, http.StatusOK, map[string]any{"logged_in": false})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logged_in": true, "user_id": sess.UserID})
}

// clientIP extracts the best-effort source IP from the request. Uses
// RemoteAddr directly so that callers cannot spoof the rate-limit key by
// sending an arbitrary X-Forwarded-For header.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

// writeJSON writes a JSON response with the given status code. Encode errors
// are intentionally swallowed — by the time the encoder writes the status has
// already been flushed and there is nothing useful the handler can do.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
