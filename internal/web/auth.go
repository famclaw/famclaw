package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

// ── Child join codes ─────────────────────────────────────────────────────────

// joinCodeAlphabet is an unambiguous 32-char alphabet (no 0/O/1/I) for
// per-child join codes. 32 chars keeps int(c) & 31 an unbiased index, so a
// code is exactly 8 × 5 = 40 bits of entropy from crypto/rand.
const joinCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// joinCodeLength is the length of a generated join code.
const joinCodeLength = 8

// generateJoinCode draws a random 8-character join code from joinCodeAlphabet.
// Codes are generated server-side (never parent-chosen) so the entropy claim
// is honest; the parent reads the returned code once and hands it over.
func generateJoinCode() (string, error) {
	buf := make([]byte, joinCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating join code: %w", err)
	}
	code := make([]byte, joinCodeLength)
	for i, c := range buf {
		code[i] = joinCodeAlphabet[int(c)&31]
	}
	return string(code), nil
}

// JoinHandler serves the child-join flow: /api/join (public — the child's
// entry point, sharing /login's per-IP rate budget) and
// /api/users/<name>/join-code (parent-only — issue/revoke the per-child
// code). The model: the parent issues a short, revocable, per-child code
// from the dashboard; the child enters (name + code) on /join and receives a
// scoped child session cookie using the SAME web_sessions store and cookie
// attributes as the parent flow. The child never sees or transmits the
// parent PIN.
//
// All failure paths of HandleJoin return the SAME 401 body — unknown user,
// parent name, unissued code, wrong code, and vault decrypt failure are
// indistinguishable — and the handler enforces the same minLatency floor as
// /login.
type JoinHandler struct {
	sessions                 *store.SessionStore
	vault                    *credstore.Vault
	getChildPINCiphertext    func(context.Context, string) ([]byte, error)
	saveChildPINCiphertext   func(context.Context, string, []byte) error
	deleteChildPINCiphertext func(context.Context, string) error
	resolveChildUserID       func(context.Context, string) (int64, error)
	limiter                  *loginLimiter
	minLatency               time.Duration
}

// NewJoinHandler constructs a JoinHandler. limiter is the SAME instance as
// the AuthHandler's (see AuthHandler.Limiter): brute force against /join and
// /login draws one shared per-IP budget, the same argument handleSetupUnlock
// already uses.
func NewJoinHandler(
	sessions *store.SessionStore,
	vault *credstore.Vault,
	getChildPINCiphertext func(context.Context, string) ([]byte, error),
	saveChildPINCiphertext func(context.Context, string, []byte) error,
	deleteChildPINCiphertext func(context.Context, string) error,
	resolveChildUserID func(context.Context, string) (int64, error),
	limiter *loginLimiter,
) *JoinHandler {
	return &JoinHandler{
		sessions:                 sessions,
		vault:                    vault,
		getChildPINCiphertext:    getChildPINCiphertext,
		saveChildPINCiphertext:   saveChildPINCiphertext,
		deleteChildPINCiphertext: deleteChildPINCiphertext,
		resolveChildUserID:       resolveChildUserID,
		limiter:                  limiter,
		minLatency:               250 * time.Millisecond,
	}
}

// HandleJoin verifies (child name + join code) against the vault row
// user_pin:<name> and, on success, issues a session cookie bound to the
// CHILD's user ID. Every failure path returns the byte-identical body
// {"error":"invalid credentials"} with 401; the 250 ms latency floor keeps
// timing from a further channel.
func (j *JoinHandler) HandleJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	defer func() {
		if d := j.minLatency - time.Since(start); d > 0 {
			time.Sleep(d)
		}
	}()

	ip := clientIP(r)
	allowed, retryAfter := j.limiter.check(ip)
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts"})
		return
	}

	// reject writes the constant-shape 401 and records the attempt against
	// the shared limiter.
	reject := func() {
		j.limiter.recordFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
	}

	var req struct {
		Name string `json:"name"`
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		reject()
		return
	}

	// (1) The named user must exist and be a child. resolveChildUserID folds
	// "unknown name" and "parent name" into one error on purpose: both map
	// to the same generic 401 below.
	userID, err := j.resolveChildUserID(r.Context(), req.Name)
	if err != nil {
		reject()
		return
	}

	// (2) The vault row for this child must exist and decrypt. sql.ErrNoRows
	// (code never issued) and vault decrypt failure (machine mismatch) are
	// both surfaced as the same generic 401 — like HandleLogin.
	ct, err := j.getChildPINCiphertext(r.Context(), req.Name)
	if err != nil {
		reject()
		return
	}
	codeHash, err := j.vault.Decrypt(ct)
	if err != nil {
		reject()
		return
	}

	// (3) Constant-time compare of the submitted code's hash.
	submitted := sha256.Sum256([]byte(req.Code))
	if subtle.ConstantTimeCompare(submitted[:], codeHash) != 1 {
		reject()
		return
	}

	j.limiter.recordSuccess(ip)

	sid, err := j.sessions.Create(r.Context(), userID, ip, r.Header.Get("User-Agent"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	// Same cookie name and attributes as the parent flow: the session is the
	// identity, and WithSession cannot tell (or care) which flow issued it.
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

// HandleJoinCode issues or revokes the join code for a named child. Mounted
// behind protectParent (parent session required). The plain code is returned
// exactly once on issue and never persisted (only its SHA-256, vault-
// encrypted, is stored); re-issuing replaces the row, so the old code is
// dead.
func (j *JoinHandler) HandleJoinCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing user name"})
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	// Name must resolve to a child; parents have no join codes and unknown
	// names are a config error the parent should see plainly (this is the
	// authenticated admin surface — clarity, not constant shape).
	if _, err := j.resolveChildUserID(r.Context(), name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a child user"})
		return
	}

	switch req.Action {
	case "issue":
		code, err := generateJoinCode()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		sum := sha256.Sum256([]byte(code))
		ct, err := j.vault.Encrypt(sum[:])
		if err != nil {
			log.Printf("[join] vault.Encrypt for %q: %v", name, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		if err := j.saveChildPINCiphertext(r.Context(), name, ct); err != nil {
			log.Printf("[join] storing join code for %q: %v", name, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"code": code})
	case "revoke":
		// Deleting a row that does not exist is a no-op success — revoking
		// twice is not an error from the operator's perspective.
		if err := j.deleteChildPINCiphertext(r.Context(), name); err != nil {
			log.Printf("[join] revoking join code for %q: %v", name, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `action must be "issue" or "revoke"`})
	}
}

// writeJSON writes a JSON response with the given status code. Encode errors
// are intentionally swallowed — by the time the encoder writes the status has
// already been flushed and there is nothing useful the handler can do.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
