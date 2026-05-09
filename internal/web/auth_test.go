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

// TestHandleLogin covers the per-request login decisions (status + body shape
// + cookie properties) in a single table. Per-attempt rate limiting and the
// minLatency floor have their own tables below because they assert behaviour
// across multiple sequential requests.
func TestHandleLogin(t *testing.T) {
	const wantErrorBody = `{"error":"invalid credentials"}`

	tests := []struct {
		name        string
		setupPIN    string // empty means "no PIN configured" (pinErr = ErrNoRows)
		userID      int64  // resolved user id; 0 leaves the default (1)
		requestPIN  string
		wantStatus  int
		wantUserID  int64  // 0 means: don't decode body for user_id
		wantBody    string // exact-match body for failure responses; "" skips
		checkCookie bool   // verify cookie attributes on success
	}{
		{
			name:        "Success",
			setupPIN:    "1234",
			requestPIN:  "1234",
			wantStatus:  http.StatusOK,
			wantUserID:  1,
			checkCookie: true,
		},
		{
			name:       "WrongPIN",
			setupPIN:   "1234",
			requestPIN: "9999",
			wantStatus: http.StatusUnauthorized,
			wantBody:   wantErrorBody,
		},
		{
			name:       "NoPINConfigured",
			setupPIN:   "", // pinErr = ErrNoRows
			requestPIN: "1234",
			wantStatus: http.StatusUnauthorized,
			wantBody:   wantErrorBody, // identical to WrongPIN — required for indistinguishability
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, cleanup := newAuthTestDeps(t)
			defer cleanup()
			if tc.setupPIN != "" {
				deps.setPIN(t, tc.setupPIN)
			} else {
				deps.pinErr = sql.ErrNoRows
			}
			if tc.userID != 0 {
				deps.userID = tc.userID
			}
			h := deps.handler()

			body, _ := json.Marshal(map[string]string{"pin": tc.requestPIN})
			req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
			req.RemoteAddr = "10.0.0.1:1234"
			rec := httptest.NewRecorder()

			h.HandleLogin(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.wantBody != "" {
				if got := strings.TrimSpace(rec.Body.String()); got != tc.wantBody {
					t.Errorf("body = %q, want %q", got, tc.wantBody)
				}
			}

			if tc.wantUserID != 0 {
				var resp struct {
					UserID int64 `json:"user_id"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
				}
				if resp.UserID != tc.wantUserID {
					t.Errorf("user_id = %d, want %d", resp.UserID, tc.wantUserID)
				}
			}

			if tc.checkCookie {
				var sessionCookie *http.Cookie
				for _, c := range rec.Result().Cookies() {
					if c.Name == "famclaw_session" {
						sessionCookie = c
						break
					}
				}
				if sessionCookie == nil {
					t.Fatalf("Set-Cookie famclaw_session missing")
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
		})
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

	tests := []struct {
		name           string
		wantStatus     int
		wantRetryAfter bool
	}{
		{name: "attempt-1", wantStatus: http.StatusUnauthorized},
		{name: "attempt-2", wantStatus: http.StatusUnauthorized},
		{name: "attempt-3", wantStatus: http.StatusUnauthorized},
		{name: "attempt-4", wantStatus: http.StatusUnauthorized},
		{name: "attempt-5", wantStatus: http.StatusUnauthorized},
		{name: "attempt-6-locked-out", wantStatus: http.StatusTooManyRequests, wantRetryAfter: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
			req.RemoteAddr = "10.0.0.99:1234"
			rec := httptest.NewRecorder()
			h.HandleLogin(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantRetryAfter && rec.Header().Get("Retry-After") == "" {
				t.Errorf("Retry-After header missing on %d", tc.wantStatus)
			}
		})
	}
}

func TestHandleLogin_MinLatency(t *testing.T) {
	tests := []struct {
		name       string
		requestPIN string
		remoteAddr string
		wantStatus int
	}{
		{name: "success", requestPIN: "1234", remoteAddr: "10.0.0.50:1234", wantStatus: http.StatusOK},
		{name: "failure", requestPIN: "9999", remoteAddr: "10.0.0.51:1234", wantStatus: http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Each subtest gets a fresh limiter so the 5-attempt budget is intact.
			deps, cleanup := newAuthTestDeps(t)
			defer cleanup()
			deps.setPIN(t, "1234")
			h := deps.handler()

			body, _ := json.Marshal(map[string]string{"pin": tc.requestPIN})
			req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
			req.RemoteAddr = tc.remoteAddr
			rec := httptest.NewRecorder()

			tStart := time.Now()
			h.HandleLogin(rec, req)
			elapsed := time.Since(tStart)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if elapsed < 200*time.Millisecond {
				t.Errorf("elapsed = %v, want >= 200ms (target 250ms)", elapsed)
			}
		})
	}
}

// ── Logout & session tests ───────────────────────────────────────────────────

// TestHandleSession exercises /session under three preconditions: no cookie
// at all, a cookie obtained via /login, and a cookie that has been
// invalidated by /logout. The logout case also asserts the clearing-cookie
// behaviour previously checked by TestHandleLogout_ClearsCookie.
func TestHandleSession(t *testing.T) {
	tests := []struct {
		name             string
		login            bool  // run /login to mint a session cookie before calling /session
		logout           bool  // run /logout against that cookie before calling /session
		userID           int64 // resolved user id when login=true; 0 keeps default (1)
		wantLoggedIn     bool
		wantUserID       int64 // expected user_id in /session body when wantLoggedIn=true
		wantClearCookie  bool  // verify /logout returned a clearing cookie
		wantLogoutStatus int   // expected /logout status when logout=true
	}{
		{
			name:         "NoCookie",
			wantLoggedIn: false,
		},
		{
			name:         "ValidCookie",
			login:        true,
			userID:       42,
			wantLoggedIn: true,
			wantUserID:   42,
		},
		{
			name:             "AfterLogoutClearsCookie",
			login:            true,
			logout:           true,
			wantLoggedIn:     false,
			wantClearCookie:  true,
			wantLogoutStatus: http.StatusNoContent,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps, cleanup := newAuthTestDeps(t)
			defer cleanup()
			h := deps.handler()
			h.minLatency = 0

			var sid string
			if tc.login {
				deps.setPIN(t, "1234")
				if tc.userID != 0 {
					deps.userID = tc.userID
				}
				body, _ := json.Marshal(map[string]string{"pin": "1234"})
				loginReq := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
				loginReq.RemoteAddr = "10.0.0.1:1234"
				loginRec := httptest.NewRecorder()
				h.HandleLogin(loginRec, loginReq)
				if loginRec.Code != http.StatusOK {
					t.Fatalf("login status = %d, want 200", loginRec.Code)
				}
				for _, c := range loginRec.Result().Cookies() {
					if c.Name == "famclaw_session" {
						sid = c.Value
					}
				}
				if sid == "" {
					t.Fatalf("no famclaw_session cookie returned by login")
				}
			}

			if tc.logout {
				logoutReq := httptest.NewRequest(http.MethodPost, "/logout", nil)
				logoutReq.AddCookie(&http.Cookie{Name: "famclaw_session", Value: sid})
				logoutRec := httptest.NewRecorder()
				h.HandleLogout(logoutRec, logoutReq)
				if logoutRec.Code != tc.wantLogoutStatus {
					t.Fatalf("logout status = %d, want %d", logoutRec.Code, tc.wantLogoutStatus)
				}
				if tc.wantClearCookie {
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
				}
			}

			sessReq := httptest.NewRequest(http.MethodGet, "/session", nil)
			if sid != "" {
				sessReq.AddCookie(&http.Cookie{Name: "famclaw_session", Value: sid})
			}
			sessRec := httptest.NewRecorder()
			h.HandleSession(sessRec, sessReq)

			if sessRec.Code != http.StatusOK {
				t.Fatalf("session status = %d, want 200", sessRec.Code)
			}
			var s map[string]any
			if err := json.Unmarshal(sessRec.Body.Bytes(), &s); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			gotLoggedIn, _ := s["logged_in"].(bool)
			if gotLoggedIn != tc.wantLoggedIn {
				t.Errorf("logged_in = %v, want %v", gotLoggedIn, tc.wantLoggedIn)
			}
			if tc.wantLoggedIn && tc.wantUserID != 0 {
				uid, _ := s["user_id"].(float64)
				if int64(uid) != tc.wantUserID {
					t.Errorf("user_id = %v, want %d", s["user_id"], tc.wantUserID)
				}
			}
		})
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func sha256Bytes(p []byte) []byte {
	sum := sha256.Sum256(p)
	return sum[:]
}
