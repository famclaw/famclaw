package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/credstore"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// newFirstBootServer builds a web Server in a pristine first-boot state:
// no users in config, no parent_pin row in the vault. This mirrors a
// freshly-shipped config.yaml that the user has not yet configured through
// the wizard.
func newFirstBootServer(t *testing.T) (*httptest.Server, *Server, *store.SessionStore) {
	t.Helper()

	tmpDir := t.TempDir()
	db, err := store.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	sessions := store.NewSessionStore(db.SQL())

	vault, err := credstore.New("test-machine-id-firstboot")
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
			WSOriginPolicy: "",
		},
		LLM: config.LLMConfig{
			Temperature:       0.7,
			MaxResponseTokens: 512,
		},
		// No users — first-boot state.
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
	s.auth = NewAuthHandler(sessions, vault, s.getParentPINCiphertext, s.resolveParentUserID)

	// No parent_pin row seeded — the vault is empty (true first-boot).

	t.Cleanup(func() { _ = s.WaitForBackground(context.Background()) })

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	return ts, s, sessions
}

// TestFirstBootPINBootstrapAllowsSettingsAccess is the regression test for
// issue #413: on a fresh install (users: []), the first-boot PIN bootstrap
// creates a session with user_id=1, but without a corresponding entry in
// s.cfg.Users the protectParent middleware returns 403 on /api/settings,
// leaving the user stuck forever in a chicken-and-egg loop.
//
// After the fix, handleSetupPIN seeds a placeholder parent when the user
// list is empty, so the bootstrap session passes the role gate.
func TestFirstBootPINBootstrapAllowsSettingsAccess(t *testing.T) {
	ts, s, _ := newFirstBootServer(t)

	// Step 1: POST /api/setup/pin to bootstrap the PIN.
	resp, err := http.Post(ts.URL+"/api/setup/pin", "application/json", strings.NewReader(`{"pin":"9999"}`))
	if err != nil {
		t.Fatalf("POST /api/setup/pin: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /api/setup/pin: status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	cookie := sessionCookieValue(resp)
	if cookie == "" {
		t.Fatal("POST /api/setup/pin: no session cookie set")
	}

	// Verify the placeholder parent was seeded.
	s.cfgMu.RLock()
	users := len(s.cfg.Users)
	s.cfgMu.RUnlock()
	if users != 1 {
		t.Fatalf("after PIN bootstrap: len(cfg.Users) = %d, want 1", users)
	}

	// Step 2: GET /api/settings with the bootstrap session.
	// Before the fix this returned 403 (protectParent could not find a
	// parent-role user for user_id=1). After the fix it returns 200.
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/settings", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "famclaw_session", Value: cookie})
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/settings: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("GET /api/settings with bootstrap session: status = %d, want 200; body: %s",
			resp2.StatusCode, string(body))
	}

	// Verify the response has the expected settings shape.
	var settings map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&settings); err != nil {
		t.Fatalf("decoding settings response: %v", err)
	}
	if _, ok := settings["llm"]; !ok {
		t.Error("GET /api/settings: response missing 'llm' key")
	}
}
