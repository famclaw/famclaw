package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/credstore"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// jsonMarshal is a test-only helper: the web package's writeJSON is
// response-side, and several tests need to build request bodies.
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// drain reads and discards a response body so the connection can be reused.
func drain(r io.Reader) {
	_, _ = io.Copy(io.Discard, r)
}

// joinSecurityDeps bundles the moving parts a join-flow test needs: the live
// server, its session store, vault, and config (so tests can seed vault
// rows or read the issued code's user back).
type joinSecurityDeps struct {
	s        *Server
	sessions *store.SessionStore
	vault    *credstore.Vault
	db       *store.DB
}

// newJoinSecurityServer builds a full web Server with session auth AND the
// child join flow wired exactly as NewServer does (shared limiter included),
// over a household of one parent and two children. The parent PIN row is
// seeded so /login and the conditionalProtect routes behave as post-first-boot.
// minLatency is dropped to 1ms on the join handler so the suite stays fast;
// the 250ms floor itself has a dedicated test (TestJoinMinLatency).
func newJoinSecurityServer(t *testing.T, wsOriginPolicy string) (*httptest.Server, *joinSecurityDeps) {
	t.Helper()

	tmpDir := t.TempDir()
	db, err := store.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	sessions := store.NewSessionStore(db.SQL())

	vault, err := credstore.New("test-machine-id-join")
	if err != nil {
		t.Fatalf("credstore.New: %v", err)
	}

	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatalf("NewEvaluator: %v", err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			Secret:         "test-secret",
			MDNSName:       "famclaw",
			WSOriginPolicy: wsOriginPolicy,
		},
		LLM: config.LLMConfig{
			Temperature:       0.7,
			MaxResponseTokens: 512,
		},
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
			{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"},
			{Name: "olivia", DisplayName: "Olivia", Role: "child", AgeGroup: "age_8_12"},
		},
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
	// Wire auth + join exactly as NewServer does.
	s.auth = NewAuthHandler(sessions, vault, s.getParentPINCiphertext, s.resolveParentUserID)
	s.join = NewJoinHandler(
		sessions,
		vault,
		s.getChildPINCiphertext,
		s.saveChildPINCiphertext,
		s.deleteChildPINCiphertext,
		s.resolveChildUserID,
		s.auth.Limiter(),
	)
	s.join.minLatency = time.Millisecond

	// Seed the parent PIN row (sha256, vault-encrypted) so the server is in
	// its post-first-boot state: /login works and the setup-wizard routes
	// are parent-gated.
	pinHash := sha256.Sum256([]byte("1234"))
	ct, err := vault.Encrypt(pinHash[:])
	if err != nil {
		t.Fatalf("vault.Encrypt parent pin: %v", err)
	}
	if _, err := db.SQL().ExecContext(context.Background(),
		`INSERT INTO vault_secrets(name, ciphertext, updated_at) VALUES('parent_pin', ?, ?)`,
		ct, time.Now().Unix()); err != nil {
		t.Fatalf("seeding parent pin row: %v", err)
	}

	// Wait for background goroutines to exit before the DB is closed.
	t.Cleanup(func() { _ = s.WaitForBackground(context.Background()) })

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, &joinSecurityDeps{s: s, sessions: sessions, vault: vault, db: db}
}

// issueCodeDirect stores a freshly generated join code for name via the
// server's own storage closures — the same path the parent endpoint uses,
// minus the HTTP hop. Returns the plain code.
func (d *joinSecurityDeps) issueCodeDirect(t *testing.T, name string) string {
	t.Helper()
	code, err := generateJoinCode()
	if err != nil {
		t.Fatalf("generateJoinCode: %v", err)
	}
	sum := sha256.Sum256([]byte(code))
	ct, err := d.vault.Encrypt(sum[:])
	if err != nil {
		t.Fatalf("vault.Encrypt: %v", err)
	}
	if err := d.s.saveChildPINCiphertext(context.Background(), name, ct); err != nil {
		t.Fatalf("saveChildPINCiphertext: %v", err)
	}
	return code
}

// postJoin POSTs (name, code) to /api/join and returns the status, body, and
// the issued session cookie value (empty when the response set none).
func postJoin(t *testing.T, ts *httptest.Server, name, code string) (int, []byte, string) {
	t.Helper()
	body, _ := jsonMarshal(map[string]string{"name": name, "code": code})
	resp, err := http.Post(ts.URL+"/api/join", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/join: %v", err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("reading join response: %v", err)
	}
	return resp.StatusCode, buf.Bytes(), sessionCookieValue(resp)
}

// loginPOST POSTs the parent PIN to /login and returns the status and the
// issued session cookie value (empty on failure).
func loginPOST(t *testing.T, ts *httptest.Server, pin string) (int, string) {
	t.Helper()
	body, _ := jsonMarshal(map[string]string{"pin": pin})
	resp, err := http.Post(ts.URL+"/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer resp.Body.Close()
	drain(resp.Body)
	return resp.StatusCode, sessionCookieValue(resp)
}

func sessionCookieValue(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == "famclaw_session" {
			return c.Value
		}
	}
	return ""
}

// TestJoinConstantShape401 (design-note T11, T12, T14): an unissued code, a
// wrong code, and a parent name all produce the SAME byte-identical 401 body
// — the join endpoint must not leak which of those states it is in.
func TestJoinConstantShape401(t *testing.T) {
	ts, deps := newJoinSecurityServer(t, "")

	// T11 — child with no code issued yet.
	status1, body1, _ := postJoin(t, ts, "emma", "WRONGCODE1")
	if status1 != http.StatusUnauthorized {
		t.Fatalf("unissued: status = %d, want 401", status1)
	}
	// T12 — code issued, wrong code submitted.
	code := deps.issueCodeDirect(t, "emma")
	if code == "" {
		t.Fatal("issued code is empty")
	}
	status2, body2, _ := postJoin(t, ts, "emma", "NOTTHECODE")
	if status2 != http.StatusUnauthorized {
		t.Fatalf("wrong code: status = %d, want 401", status2)
	}

	// T14 — naming the parent with any code.
	status3, body3, _ := postJoin(t, ts, "parent", "WRONGCODE3")
	if status3 != http.StatusUnauthorized {
		t.Fatalf("parent name: status = %d, want 401", status3)
	}

	want := []byte(`{"error":"invalid credentials"}` + "\n") // writeJSON uses json.Encoder
	for i, b := range [][]byte{body1, body2, body3} {
		if !bytes.Equal(b, want) {
			t.Errorf("case %d: body = %q, want byte-identical %q", i+1, b, want)
		}
	}
}

// TestJoinSuccess (design-note T13): a correct code seats a session cookie
// bound to the CHILD's user ID, and /session reports that child.
func TestJoinSuccess(t *testing.T) {
	ts, deps := newJoinSecurityServer(t, "")
	code := deps.issueCodeDirect(t, "emma")

	status, _, sid := postJoin(t, ts, "emma", code)
	if status != http.StatusOK {
		t.Fatalf("join status = %d, want 200", status)
	}
	if sid == "" {
		t.Fatal("expected a famclaw_session cookie on successful join")
	}

	// The session must carry emma's synthesised user ID (config index 1 + 1).
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/session", nil)
	req.AddCookie(&http.Cookie{Name: "famclaw_session", Value: sid})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /session: %v", err)
	}
	defer resp.Body.Close()
	var sess struct {
		LoggedIn bool  `json:"logged_in"`
		UserID   int64 `json:"user_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatalf("decoding /session: %v", err)
	}
	if !sess.LoggedIn || sess.UserID != 2 {
		t.Errorf("/session = logged_in=%v user_id=%d, want true/2 (emma)", sess.LoggedIn, sess.UserID)
	}
}

// TestJoinRateLimitSharedWithLogin (design-note T15): the join endpoint
// shares /login's per-IP budget — 4 failed /login attempts plus 1 failed
// /join attempt lock the IP out, and the next attempt gets 429 + Retry-After.
func TestJoinRateLimitSharedWithLogin(t *testing.T) {
	ts, _ := newJoinSecurityServer(t, "")

	// 4 failed parent logins (wrong PIN; the PIN row is seeded).
	for i := 0; i < 4; i++ {
		status, _ := loginPOST(t, ts, "9999")
		if status != http.StatusUnauthorized {
			t.Fatalf("login attempt %d: status = %d, want 401", i+1, status)
		}
	}
	// 5th failure on the shared budget comes from /join.
	status, _, _ := postJoin(t, ts, "emma", "WRONGCODE5")
	if status != http.StatusUnauthorized {
		t.Fatalf("join attempt after 4 login failures: status = %d, want 401", status)
	}
	// Now locked out: 429 with Retry-After.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/join",
		bytes.NewReader([]byte(`{"name":"emma","code":"WRONGCODE6"}`)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/join (locked out): %v", err)
	}
	defer resp.Body.Close()
	drain(resp.Body)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("locked-out join: status = %d, want 429", resp.StatusCode)
	}
	if ra := resp.Header.Get("Retry-After"); ra == "" {
		t.Error("expected Retry-After header on 429")
	}
}

// TestJoinIssueRevokeLifecycle (design-note T17): a parent session can issue
// a code the child then joins with; revoking the code kills it.
func TestJoinIssueRevokeLifecycle(t *testing.T) {
	ts, _ := newJoinSecurityServer(t, "")
	_, parentSID := loginPOST(t, ts, "1234")
	if parentSID == "" {
		t.Fatal("parent login failed — cannot test the parent-only endpoint")
	}

	code, ok := parentJoinCode(t, ts, parentSID, "emma", "issue")
	if !ok {
		t.Fatal("issue failed")
	}
	// The code is 8 chars from the unambiguous alphabet.
	if len(code) != 8 || strings.ContainsAny(code, "0O1I") {
		t.Fatalf("issued code %q is not 8 chars of the unambiguous alphabet", code)
	}

	// The child joins with it.
	status, _, _ := postJoin(t, ts, "emma", code)
	if status != http.StatusOK {
		t.Fatalf("join with issued code: status = %d, want 200", status)
	}

	// Revoke, then the same code is rejected.
	if _, ok := parentJoinCode(t, ts, parentSID, "emma", "revoke"); !ok {
		t.Fatal("revoke failed")
	}
	status, _, _ = postJoin(t, ts, "emma", code)
	if status != http.StatusUnauthorized {
		t.Fatalf("join after revoke: status = %d, want 401", status)
	}
}

// TestJoinReissueReplaces (design-note T23): issuing a second code for the
// same child invalidates the first.
func TestJoinReissueReplaces(t *testing.T) {
	ts, _ := newJoinSecurityServer(t, "")
	_, parentSID := loginPOST(t, ts, "1234")
	if parentSID == "" {
		t.Fatal("parent login failed")
	}

	codeA, ok := parentJoinCode(t, ts, parentSID, "olivia", "issue")
	if !ok {
		t.Fatal("issue A failed")
	}
	if status, _, _ := postJoin(t, ts, "olivia", codeA); status != http.StatusOK {
		t.Fatalf("join with A: status = %d, want 200", status)
	}

	codeB, ok := parentJoinCode(t, ts, parentSID, "olivia", "issue")
	if !ok || codeB == codeA {
		t.Fatalf("issue B failed (A=%q B=%q)", codeA, codeB)
	}
	if status, _, _ := postJoin(t, ts, "olivia", codeA); status != http.StatusUnauthorized {
		t.Errorf("join with replaced A: status = %d, want 401", status)
	}
	if status, _, _ := postJoin(t, ts, "olivia", codeB); status != http.StatusOK {
		t.Errorf("join with B: status = %d, want 200", status)
	}
}

// TestJoinCrossUserCode (design-note T24): a code is bound to its child's
// row — submitting olivia's code under emma's name is rejected.
func TestJoinCrossUserCode(t *testing.T) {
	ts, deps := newJoinSecurityServer(t, "")
	emmaCode := deps.issueCodeDirect(t, "emma")
	oliviaCode := deps.issueCodeDirect(t, "olivia")

	if status, _, _ := postJoin(t, ts, "emma", oliviaCode); status != http.StatusUnauthorized {
		t.Errorf("olivia's code under emma: status = %d, want 401", status)
	}
	if status, _, _ := postJoin(t, ts, "olivia", emmaCode); status != http.StatusUnauthorized {
		t.Errorf("emma's code under olivia: status = %d, want 401", status)
	}
	// Sanity: each code works for its own child (the rows are per-user, not
	// a global secret that merely got mixed up).
	if status, _, _ := postJoin(t, ts, "emma", emmaCode); status != http.StatusOK {
		t.Errorf("emma with her own code: status = %d, want 200", status)
	}
}

// TestJoinCodeAlphabet: every generated code is 8 chars drawn only from the
// unambiguous alphabet (no 0/O/1/I), so the code survives being read aloud.
func TestJoinCodeAlphabet(t *testing.T) {
	for i := 0; i < 200; i++ {
		code, err := generateJoinCode()
		if err != nil {
			t.Fatalf("generateJoinCode: %v", err)
		}
		if len(code) != 8 {
			t.Fatalf("code %q is %d chars, want 8", code, len(code))
		}
		for _, r := range code {
			if !strings.ContainsRune(joinCodeAlphabet, r) {
				t.Fatalf("code %q contains out-of-alphabet char %q", code, r)
			}
		}
	}
}

// TestChildPINRowName pins the vault row naming: the "user_pin:" prefix must
// never produce the parent's "parent_pin" row (risk note 5 — a collision
// would let a child join flow touch the parent PIN).
func TestChildPINRowName(t *testing.T) {
	names := []string{"", "parent", "pin", "parent_pin", "user_pin:x", "emma"}
	for _, name := range names {
		t.Run("name="+name, func(t *testing.T) {
			got := childPINRowName(name)
			if got != "user_pin:"+name {
				t.Errorf("childPINRowName(%q) = %q, want %q", name, got, "user_pin:"+name)
			}
			if got == "parent_pin" {
				t.Errorf("childPINRowName(%q) collided with the parent PIN row", name)
			}
		})
	}
}

// TestJoinMinLatency mirrors TestHandleLogin_MinLatency: a failed join
// response takes at least the 250ms floor, so timing cannot distinguish the
// failure reasons.
func TestJoinMinLatency(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "minlat.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessions := store.NewSessionStore(db.SQL())

	vault, err := credstore.New("test-machine-id-minlat")
	if err != nil {
		t.Fatalf("credstore.New: %v", err)
	}

	// A bare handler (not the fast test fixture) carries the default 250ms.
	h := NewJoinHandler(
		sessions,
		vault,
		func(context.Context, string) ([]byte, error) { return nil, sql.ErrNoRows },
		func(context.Context, string, []byte) error { return nil },
		func(context.Context, string) error { return nil },
		func(context.Context, string) (int64, error) { return 2, nil },
		newLoginLimiter(),
	)
	ts := httptest.NewServer(http.HandlerFunc(h.HandleJoin))
	defer ts.Close()

	body, _ := jsonMarshal(map[string]string{"name": "emma", "code": "XK7P2M9Q"})
	start := time.Now()
	resp, err := http.Post(ts.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/join: %v", err)
	}
	defer resp.Body.Close()
	drain(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Errorf("join failed in %v, want >= 250ms latency floor", elapsed)
	}
}

// parentJoinCode calls the parent-only join-code endpoint and returns the
// issued code (ok=false on any non-200).
func parentJoinCode(t *testing.T, ts *httptest.Server, parentSID, name, action string) (string, bool) {
	t.Helper()
	body, _ := jsonMarshal(map[string]string{"action": action})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/users/"+name+"/join-code", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "famclaw_session", Value: parentSID})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST join-code: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		t.Fatalf("join-code %q: status = %d body = %q", action, resp.StatusCode, buf.String())
	}
	var out struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding join-code response: %v", err)
	}
	return out.Code, true
}
