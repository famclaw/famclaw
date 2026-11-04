package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/credstore"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/gorilla/websocket"
)

// Terra F7 regression: web sessions are bound to the user's stable config
// NAME, not to a position in the config array. A parent settings update can
// reorder, replace or remove the user list at any time; under the old
// positional principal a child's cookie could map onto the parent slot (or
// onto a different user) after such an update. These tests pin the safe
// behaviour: identity and privilege follow the name, and unresolvable or
// pre-migration (positional) sessions are revoked, never guessed.

// principalTestUsers is the fixture household: one parent, two children.
func principalTestUsers() []config.UserConfig {
	return []config.UserConfig{
		{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
		{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"},
		{Name: "olivia", DisplayName: "Olivia", Role: "child", AgeGroup: "age_8_12"},
	}
}

// newPrincipalTestServer builds a live web server exactly as
// newJoinSecurityServer does (auth + join wired, parent PIN seeded), but over
// a caller-chosen DB file and user list so tests can restart the server on
// the same database. The returned *Server exposes cfgMu for config mutation
// under the production lock.
func newPrincipalTestServer(t *testing.T, dbPath string, users []config.UserConfig) (*httptest.Server, *Server, *store.SessionStore, *store.DB) {
	t.Helper()

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	// Close the DB last — registered first so t.Cleanup (LIFO) runs it last.
	t.Cleanup(func() { _ = db.Close() })

	sessions := store.NewSessionStore(db.SQL())

	vault, err := credstore.New("test-machine-id-principal")
	if err != nil {
		t.Fatalf("credstore.New: %v", err)
	}

	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatalf("NewEvaluator: %v", err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			Secret:   "test-secret",
			MDNSName: "famclaw",
		},
		LLM: config.LLMConfig{
			Temperature:       0.7,
			MaxResponseTokens: 512,
		},
		Users: users,
	}
	cfg.Tools.SandboxRoot = t.TempDir()

	identStore := identity.NewStore(db)
	s := &Server{
		cfg:        cfg,
		db:         db,
		identStore: identStore,
		evaluator:  ev,
		clf:        classifier.New(),
		sessions:   sessions,
		vault:      vault,
		notifier:   notify.NewMultiNotifier(cfg, identStore, func(ctx context.Context, gw, chatID, text string) error { return nil }),
		cfgMu:      sync.RWMutex{},
		clients:    make(map[*websocket.Conn]*wsClient),
	}
	s.upgrader = websocket.Upgrader{CheckOrigin: s.allowedOrigin}
	s.auth = NewAuthHandler(sessions, vault, s.getParentPINCiphertext, s.resolveParentPrincipal)
	s.join = NewJoinHandler(
		sessions,
		vault,
		s.getChildPINCiphertext,
		s.saveChildPINCiphertext,
		s.deleteChildPINCiphertext,
		s.resolveChildPrincipal,
		s.auth.Limiter(),
	)

	// Seed the parent PIN row so the server is in its post-first-boot state.
	pinHash := sha256.Sum256([]byte("1234"))
	ct, err := vault.Encrypt(pinHash[:])
	if err != nil {
		t.Fatalf("vault.Encrypt parent pin: %v", err)
	}
	if _, err := db.SQL().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO vault_secrets(name, ciphertext, updated_at) VALUES('parent_pin', ?, ?)`,
		ct, time.Now().Unix()); err != nil {
		t.Fatalf("seeding parent pin row: %v", err)
	}

	t.Cleanup(func() { _ = s.WaitForBackground(context.Background()) })

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, s, sessions, db
}

// getWithSession does a GET carrying the session cookie and returns the
// status code and the (fully read) body.
func getWithSession(t *testing.T, ts *httptest.Server, path, sid string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "famclaw_session", Value: sid})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestSessionSurvivesUserReorder (F7): a child session stays that exact child
// when the household order changes — it cannot become a parent or another
// user, and the parent's session keeps working under its own name.
func TestSessionSurvivesUserReorder(t *testing.T) {
	ts, s, sessions, _ := newPrincipalTestServer(t, filepath.Join(t.TempDir(), "test.db"), principalTestUsers())
	ctx := context.Background()

	emmaSess, err := sessions.Create(ctx, "emma", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create (emma): %v", err)
	}
	parentSess, err := sessions.Create(ctx, "parent", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create (parent): %v", err)
	}

	// Control before the reorder: emma is a child, parent is a manager.
	if code, _ := getWithSession(t, ts, "/api/users", emmaSess); code != http.StatusForbidden {
		t.Fatalf("pre-reorder emma GET /api/users = %d, want 403", code)
	}
	if code, _ := getWithSession(t, ts, "/api/users", parentSess); code != http.StatusOK {
		t.Fatalf("pre-reorder parent GET /api/users = %d, want 200", code)
	}

	// Parent settings update: reorder the household (emma first, parent
	// last) — the same shape a /api/settings save produces.
	s.cfgMu.Lock()
	s.cfg.Users = []config.UserConfig{
		{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"},
		{Name: "olivia", DisplayName: "Olivia", Role: "child", AgeGroup: "age_8_12"},
		{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
	}
	s.cfgMu.Unlock()

	// The SAME live cookies: emma is still exactly emma (still 403 on the
	// admin route — not promoted onto any parent slot) and parent still manages.
	if code, body := getWithSession(t, ts, "/api/users", emmaSess); code != http.StatusForbidden {
		t.Fatalf("post-reorder emma GET /api/users = %d, want 403 (body=%s)", code, body)
	}
	if code, _ := getWithSession(t, ts, "/api/users", parentSess); code != http.StatusOK {
		t.Fatalf("post-reorder parent GET /api/users = %d, want 200", code)
	}
	// /session still identifies the child by name, not by position.
	code, body := getWithSession(t, ts, "/session", emmaSess)
	if code != http.StatusOK {
		t.Fatalf("/session = %d, want 200", code)
	}
	var sessInfo map[string]any
	if err := json.Unmarshal([]byte(body), &sessInfo); err != nil {
		t.Fatalf("unmarshal /session: %v", err)
	}
	if sessInfo["user_name"] != "emma" {
		t.Errorf("emma /session user_name = %v, want 'emma'", sessInfo["user_name"])
	}
}

// TestSessionSurvivesUserReplacementAndRemoval (F7): the user list can be
// wholesale replaced and a user removed. Surviving users keep their sessions;
// a removed user's session no longer resolves and is denied on every
// authorization surface rather than mapped onto another user.
func TestSessionSurvivesUserReplacementAndRemoval(t *testing.T) {
	ts, s, sessions, _ := newPrincipalTestServer(t, filepath.Join(t.TempDir(), "test.db"), principalTestUsers())
	ctx := context.Background()

	oliviaSess, err := sessions.Create(ctx, "olivia", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create (olivia): %v", err)
	}
	emmaSess, err := sessions.Create(ctx, "emma", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create (emma): %v", err)
	}

	// Replace the household: olivia is removed, emma and parent remain in a
	// different array shape (parent now first).
	s.cfgMu.Lock()
	s.cfg.Users = []config.UserConfig{
		{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
		{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"},
	}
	s.cfgMu.Unlock()

	// emma's session survives the replacement: still her, still a child.
	if code, body := getWithSession(t, ts, "/api/users", emmaSess); code != http.StatusForbidden {
		t.Fatalf("post-replacement emma GET /api/users = %d, want 403 (body=%s)", code, body)
	}
	code, _ := getWithSession(t, ts, "/session", emmaSess)
	if code != http.StatusOK {
		t.Fatalf("/session = %d, want 200", code)
	}

	// olivia was removed: her cookie must not map onto emma or parent — it is
	// unresolvable and gets 403 on the authorization surfaces.
	if code, body := getWithSession(t, ts, "/api/users", oliviaSess); code != http.StatusForbidden {
		t.Fatalf("removed-user GET /api/users = %d, want 403 (body=%s)", code, body)
	}
	if code, body := getWithSession(t, ts, "/api/conversations?user=emma", oliviaSess); code != http.StatusForbidden {
		t.Fatalf("removed-user GET /api/conversations = %d, want 403 (body=%s)", code, body)
	}
}

// TestLegacyPositionalSessionRevoked (F7 safe revocation): a session row whose
// principal name is empty was created before the name-bound principal column
// existed (it was bound by config-array position, which is not safe to trust
// after a reorder). It must be refused — the holder re-authenticates — never
// mapped onto whichever user now occupies that position.
func TestLegacyPositionalSessionRevoked(t *testing.T) {
	ts, _, _, db := newPrincipalTestServer(t, filepath.Join(t.TempDir(), "test.db"), principalTestUsers())
	ctx := context.Background()

	// Insert a legacy row exactly as pre-migration code did: positional
	// user_id, empty name. 43-char URL-safe id so it passes the cookie shape.
	legacyID := "legacyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	now := time.Now().Unix()
	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO web_sessions(id, user_id, user_name, created_at, expires_at, last_seen, ip, user_agent)
		VALUES(?, 2, '', ?, ?, ?, '127.0.0.1', 'test')`, legacyID, now, now+3600, now); err != nil {
		t.Fatalf("insert legacy session row: %v", err)
	}

	// The middleware must revoke it on any gated route.
	if code, _ := getWithSession(t, ts, "/api/users", legacyID); code != http.StatusUnauthorized {
		t.Fatalf("legacy positional session GET /api/users = %d, want 401", code)
	}
	// /session reports it as logged out (UI re-auth path), not as some user.
	code, body := getWithSession(t, ts, "/session", legacyID)
	if code != http.StatusOK {
		t.Fatalf("/session = %d, want 200", code)
	}
	var sessInfo map[string]any
	if err := json.Unmarshal([]byte(body), &sessInfo); err != nil {
		t.Fatalf("unmarshal /session: %v", err)
	}
	if sessInfo["logged_in"] != false {
		t.Errorf("legacy positional session /session logged_in = %v, want false", sessInfo["logged_in"])
	}
}

// TestSessionSurvivesServerRestart (F7): the principal lives in SQLite, so a
// session bound to a user name still authenticates after a process restart on
// the same database — even with the household reordered meanwhile — no
// positional re-mapping, no lost identity.
func TestSessionSurvivesServerRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart.db")
	ctx := context.Background()

	// "First process": emma's session is created with the original household
	// order.
	ts1, _, sessions, _ := newPrincipalTestServer(t, dbPath, principalTestUsers())
	emmaSess, err := sessions.Create(ctx, "emma", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create (emma): %v", err)
	}
	if code, _ := getWithSession(t, ts1, "/api/users", emmaSess); code != http.StatusForbidden {
		t.Fatalf("pre-restart emma GET /api/users = %d, want 403", code)
	}

	// "Second process": a fresh server over the SAME database file, with the
	// in-memory config re-read in the reordered shape (as a settings save
	// would have persisted it). The persisted session must still resolve to
	// emma — not to whichever user now occupies her old position.
	ts2, _, _, _ := newPrincipalTestServer(t, dbPath, []config.UserConfig{
		{Name: "olivia", DisplayName: "Olivia", Role: "child", AgeGroup: "age_8_12"},
		{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
		{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"},
	})
	if code, body := getWithSession(t, ts2, "/api/users", emmaSess); code != http.StatusForbidden {
		t.Fatalf("post-restart emma GET /api/users = %d, want 403 (body=%s)", code, body)
	}
	code, body := getWithSession(t, ts2, "/session", emmaSess)
	if code != http.StatusOK {
		t.Fatalf("/session = %d, want 200", code)
	}
	var sessInfo map[string]any
	if err := json.Unmarshal([]byte(body), &sessInfo); err != nil {
		t.Fatalf("unmarshal /session: %v", err)
	}
	if sessInfo["user_name"] != "emma" {
		t.Errorf("post-restart emma /session user_name = %v, want 'emma'", sessInfo["user_name"])
	}
}
