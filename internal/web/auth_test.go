package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/credstore"
	"github.com/famclaw/famclaw/internal/store"

	_ "modernc.org/sqlite"
)

const testMachineID = "test-machine-id-deterministic"

// newAuthTestDeps spins up a SessionStore, Vault, and the closures NewAuthHandler
// expects. The caller picks the PIN ciphertext (via setPIN) and the resolved
// user id (via setUser); both default to a working "1234"/userID=1 pair.
type authTestDeps struct {
	sessions *store.SessionStore
	vault    *credstore.Vault
	rawDB    *sql.DB
	storeDB  *store.DB
	pinErr   error
	pinCT    []byte
	userID   int64
	userErr  error
}

func newAuthTestDeps(t *testing.T) (*authTestDeps, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// First create the schema via store.Open.
	storeDB, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	// Then open a raw *sql.DB on the same file for the SessionStore. WAL mode
	// allows the two handles to coexist for the duration of the test.
	raw, err := sql.Open("sqlite", dbPath+"?_journal=WAL&_timeout=5000&_fk=true")
	if err != nil {
		_ = storeDB.Close()
		t.Fatalf("sql.Open: %v", err)
	}

	vault, err := credstore.New(testMachineID)
	if err != nil {
		_ = raw.Close()
		_ = storeDB.Close()
		t.Fatalf("credstore.New: %v", err)
	}

	deps := &authTestDeps{
		sessions: store.NewSessionStore(raw),
		vault:    vault,
		rawDB:    raw,
		storeDB:  storeDB,
		userID:   1,
	}

	cleanup := func() {
		_ = raw.Close()
		_ = storeDB.Close()
	}
	return deps, cleanup
}

// handler builds an AuthHandler whose getPIN/resolveUserID closures defer to
// the current values on deps, so individual tests can mutate behaviour after
// construction.
func (d *authTestDeps) handler() *AuthHandler {
	return NewAuthHandler(
		d.sessions,
		d.vault,
		func(context.Context) ([]byte, error) {
			if d.pinErr != nil {
				return nil, d.pinErr
			}
			return d.pinCT, nil
		},
		func(context.Context) (int64, error) {
			return d.userID, d.userErr
		},
	)
}

// setPIN encrypts the SHA-256 of pin under the vault and wires the ciphertext
// into the deps so the handler will accept that PIN.
func (d *authTestDeps) setPIN(t *testing.T, pin string) {
	t.Helper()
	hash := sha256Sum(pin)
	ct, err := d.vault.Encrypt(hash)
	if err != nil {
		t.Fatalf("vault.Encrypt: %v", err)
	}
	d.pinCT = ct
	d.pinErr = nil
}

func sha256Sum(s string) []byte {
	// Tests intentionally avoid pulling in crypto/sha256 directly so the file
	// has only the imports it actually needs in the failure paths. Call the
	// helper through the std lib via a tiny wrapper.
	return sha256Bytes([]byte(s))
}

// ── Limiter tests ────────────────────────────────────────────────────────────

func TestLoginLimiter_AllowsUntilThreshold(t *testing.T) {
	l := newLoginLimiter()
	ip := "10.0.0.1"
	for i := 0; i < 5; i++ {
		allowed, _ := l.check(ip)
		if !allowed {
			t.Fatalf("attempt #%d: check returned allowed=false, want true", i+1)
		}
		l.recordFailure(ip)
	}
	allowed, retryAfter := l.check(ip)
	if allowed {
		t.Errorf("6th check: allowed=true, want false")
	}
	if retryAfter <= 0 {
		t.Errorf("6th check: retryAfter=%v, want >0", retryAfter)
	}
}

func TestLoginLimiter_LockoutExpires(t *testing.T) {
	l := newLoginLimiter()
	t0 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	clock := t0
	l.now = func() time.Time { return clock }

	ip := "10.0.0.2"
	for i := 0; i < 5; i++ {
		_, _ = l.check(ip)
		l.recordFailure(ip)
	}
	allowed, _ := l.check(ip)
	if allowed {
		t.Fatalf("immediately after lockout: allowed=true, want false")
	}

	// Advance past lockout.
	clock = t0.Add(2 * time.Minute)
	allowed, _ = l.check(ip)
	if !allowed {
		t.Errorf("after lockout expiry: allowed=false, want true")
	}
}

func TestLoginLimiter_PerIPIsolated(t *testing.T) {
	l := newLoginLimiter()
	ip1 := "10.0.0.10"
	ip2 := "10.0.0.20"
	for i := 0; i < 5; i++ {
		_, _ = l.check(ip1)
		l.recordFailure(ip1)
	}
	if allowed, _ := l.check(ip1); allowed {
		t.Fatalf("ip1: allowed=true after lockout, want false")
	}
	if allowed, _ := l.check(ip2); !allowed {
		t.Errorf("ip2: allowed=false, want true (per-IP isolation)")
	}
}

// ── Login tests ──────────────────────────────────────────────────────────────

func TestHandleLogin_Success(t *testing.T) {
	deps, cleanup := newAuthTestDeps(t)
	defer cleanup()
	deps.setPIN(t, "1234")
	h := deps.handler()

	body, _ := json.Marshal(map[string]string{"pin": "1234"})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	req.RemoteAddr = "10.0.0.1:1234"
	rec := httptest.NewRecorder()

	h.HandleLogin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		UserID int64 `json:"user_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if resp.UserID != 1 {
		t.Errorf("user_id = %d, want 1", resp.UserID)
	}

	cookies := rec.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "famclaw_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatalf("Set-Cookie famclaw_session missing; got cookies=%v", cookies)
	}
	if !sessionCookie.HttpOnly {
		t.Errorf("HttpOnly = false, want true")
	}
	if sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", sessionCookie.SameSite)
	}
	if sessionCookie.MaxAge != 604800 {
		t.Errorf("MaxAge = %d, want 604800", sessionCookie.MaxAge)
	}
	if sessionCookie.Secure {
		t.Errorf("Secure = true on plain HTTP test, want false")
	}
}

func TestHandleLogin_WrongPIN(t *testing.T) {
	deps, cleanup := newAuthTestDeps(t)
	defer cleanup()
	deps.setPIN(t, "1234")
	h := deps.handler()

	body, _ := json.Marshal(map[string]string{"pin": "9999"})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	req.RemoteAddr = "10.0.0.1:1234"
	rec := httptest.NewRecorder()

	h.HandleLogin(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	got := strings.TrimSpace(rec.Body.String())
	want := `{"error":"invalid credentials"}`
	if got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestHandleLogin_NoPINConfigured(t *testing.T) {
	deps, cleanup := newAuthTestDeps(t)
	defer cleanup()
	deps.pinErr = sql.ErrNoRows
	h := deps.handler()

	body, _ := json.Marshal(map[string]string{"pin": "1234"})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	req.RemoteAddr = "10.0.0.1:1234"
	rec := httptest.NewRecorder()

	h.HandleLogin(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	// Build the wrong-PIN response in a separate exchange to compare bytes.
	deps2, cleanup2 := newAuthTestDeps(t)
	defer cleanup2()
	deps2.setPIN(t, "1234")
	h2 := deps2.handler()
	body2, _ := json.Marshal(map[string]string{"pin": "9999"})
	req2 := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body2))
	req2.RemoteAddr = "10.0.0.2:1234"
	rec2 := httptest.NewRecorder()
	h2.HandleLogin(rec2, req2)

	if !bytes.Equal(rec.Body.Bytes(), rec2.Body.Bytes()) {
		t.Errorf("no-PIN body %q != wrong-PIN body %q", rec.Body.String(), rec2.Body.String())
	}
}

func TestHandleLogin_RateLimit(t *testing.T) {
	deps, cleanup := newAuthTestDeps(t)
	defer cleanup()
	deps.setPIN(t, "1234")
	// Disable the latency floor so the test runs fast.
	h := deps.handler()
	h.minLatency = 0

	body, _ := json.Marshal(map[string]string{"pin": "9999"})

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
		req.RemoteAddr = "10.0.0.99:1234"
		rec := httptest.NewRecorder()
		h.HandleLogin(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt #%d: status = %d, want 401", i+1, rec.Code)
		}
	}
	// 6th request should be rate-limited.
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	req.RemoteAddr = "10.0.0.99:1234"
	rec := httptest.NewRecorder()
	h.HandleLogin(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Errorf("Retry-After header missing on 429")
	}
}

func TestHandleLogin_MinLatency(t *testing.T) {
	deps, cleanup := newAuthTestDeps(t)
	defer cleanup()
	deps.setPIN(t, "1234")
	h := deps.handler()

	// Success path.
	bodyOK, _ := json.Marshal(map[string]string{"pin": "1234"})
	reqOK := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(bodyOK))
	reqOK.RemoteAddr = "10.0.0.50:1234"
	recOK := httptest.NewRecorder()

	tStart := time.Now()
	h.HandleLogin(recOK, reqOK)
	successElapsed := time.Since(tStart)

	if recOK.Code != http.StatusOK {
		t.Fatalf("success status = %d, want 200", recOK.Code)
	}
	if successElapsed < 200*time.Millisecond {
		t.Errorf("success elapsed = %v, want >= 200ms (target 250ms)", successElapsed)
	}

	// Failure path on a fresh limiter so the 5-attempt budget is intact.
	deps2, cleanup2 := newAuthTestDeps(t)
	defer cleanup2()
	deps2.setPIN(t, "1234")
	h2 := deps2.handler()

	bodyBad, _ := json.Marshal(map[string]string{"pin": "9999"})
	reqBad := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(bodyBad))
	reqBad.RemoteAddr = "10.0.0.51:1234"
	recBad := httptest.NewRecorder()

	tStart = time.Now()
	h2.HandleLogin(recBad, reqBad)
	failureElapsed := time.Since(tStart)

	if recBad.Code != http.StatusUnauthorized {
		t.Fatalf("failure status = %d, want 401", recBad.Code)
	}
	if failureElapsed < 200*time.Millisecond {
		t.Errorf("failure elapsed = %v, want >= 200ms (target 250ms)", failureElapsed)
	}
}

// ── Logout & session tests ───────────────────────────────────────────────────

func TestHandleLogout_ClearsCookie(t *testing.T) {
	deps, cleanup := newAuthTestDeps(t)
	defer cleanup()
	deps.setPIN(t, "1234")
	h := deps.handler()
	h.minLatency = 0

	// Login.
	body, _ := json.Marshal(map[string]string{"pin": "1234"})
	loginReq := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	loginReq.RemoteAddr = "10.0.0.1:1234"
	loginRec := httptest.NewRecorder()
	h.HandleLogin(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200", loginRec.Code)
	}
	cookies := loginRec.Result().Cookies()
	var sid string
	for _, c := range cookies {
		if c.Name == "famclaw_session" {
			sid = c.Value
		}
	}
	if sid == "" {
		t.Fatalf("no famclaw_session cookie returned by login")
	}

	// Logout.
	logoutReq := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutReq.AddCookie(&http.Cookie{Name: "famclaw_session", Value: sid})
	logoutRec := httptest.NewRecorder()
	h.HandleLogout(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", logoutRec.Code)
	}
	var clearing *http.Cookie
	for _, c := range logoutRec.Result().Cookies() {
		if c.Name == "famclaw_session" {
			clearing = c
		}
	}
	if clearing == nil {
		t.Fatalf("logout did not set a clearing cookie")
	}
	if clearing.MaxAge != -1 && clearing.MaxAge != 0 {
		t.Errorf("clearing MaxAge = %d, want -1 or 0", clearing.MaxAge)
	}

	// Subsequent /session with the old cookie should report logged_in:false.
	sessReq := httptest.NewRequest(http.MethodGet, "/session", nil)
	sessReq.AddCookie(&http.Cookie{Name: "famclaw_session", Value: sid})
	sessRec := httptest.NewRecorder()
	h.HandleSession(sessRec, sessReq)
	if sessRec.Code != http.StatusOK {
		t.Fatalf("session status = %d, want 200", sessRec.Code)
	}
	var s map[string]any
	if err := json.Unmarshal(sessRec.Body.Bytes(), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, _ := s["logged_in"].(bool); v {
		t.Errorf("logged_in = true after logout, want false")
	}
}

func TestHandleSession_NoCookie(t *testing.T) {
	deps, cleanup := newAuthTestDeps(t)
	defer cleanup()
	h := deps.handler()

	req := httptest.NewRequest(http.MethodGet, "/session", nil)
	rec := httptest.NewRecorder()
	h.HandleSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var s map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, _ := s["logged_in"].(bool); v {
		t.Errorf("logged_in = true with no cookie, want false")
	}
}

func TestHandleSession_ValidCookie(t *testing.T) {
	deps, cleanup := newAuthTestDeps(t)
	defer cleanup()
	deps.setPIN(t, "1234")
	deps.userID = 42
	h := deps.handler()
	h.minLatency = 0

	body, _ := json.Marshal(map[string]string{"pin": "1234"})
	loginReq := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	loginReq.RemoteAddr = "10.0.0.1:1234"
	loginRec := httptest.NewRecorder()
	h.HandleLogin(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200", loginRec.Code)
	}
	var sid string
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == "famclaw_session" {
			sid = c.Value
		}
	}
	if sid == "" {
		t.Fatalf("no session cookie set on login")
	}

	sessReq := httptest.NewRequest(http.MethodGet, "/session", nil)
	sessReq.AddCookie(&http.Cookie{Name: "famclaw_session", Value: sid})
	sessRec := httptest.NewRecorder()
	h.HandleSession(sessRec, sessReq)

	if sessRec.Code != http.StatusOK {
		t.Fatalf("session status = %d, want 200", sessRec.Code)
	}
	var s map[string]any
	if err := json.Unmarshal(sessRec.Body.Bytes(), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, _ := s["logged_in"].(bool); !v {
		t.Errorf("logged_in = false with valid cookie, want true")
	}
	uid, _ := s["user_id"].(float64)
	if int64(uid) != 42 {
		t.Errorf("user_id = %v, want 42", s["user_id"])
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func sha256Bytes(p []byte) []byte {
	sum := sha256.Sum256(p)
	return sum[:]
}
