package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// newChatSecurityServer builds a minimal web Server with session auth wired in,
// sufficient to exercise /api/chat's security properties.
func newChatSecurityServer(t *testing.T) (*httptest.Server, *store.SessionStore) {
	t.Helper()

	tmpDir := t.TempDir()
	db, err := store.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	// Close the DB last — registered first so t.Cleanup (LIFO) runs it last.
	t.Cleanup(func() { _ = db.Close() })

	sessions := store.NewSessionStore(db.SQL())

	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatalf("NewEvaluator: %v", err)
	}
	clf := classifier.New()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Secret:   "test-secret",
			MDNSName: "famclaw",
		},
		LLM: config.LLMConfig{
			Temperature:       0.7,
			MaxResponseTokens: 512,
		},
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
			{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"},
		},
	}
	cfg.Tools.SandboxRoot = t.TempDir()

	identStore := identity.NewStore(db)

	s := &Server{
		cfg:        cfg,
		db:         db,
		identStore: identStore,
		evaluator:  ev,
		clf:        clf,
		sessions:   sessions,
		notifier:   notify.NewMultiNotifier(cfg, identStore, func(ctx context.Context, gw, chatID, text string) error { return nil }),
		cfgMu:      sync.RWMutex{},
		clients:    make(map[*websocket.Conn]*wsClient),
	}
	s.upgrader = websocket.Upgrader{CheckOrigin: s.allowedOrigin}

	// Wait for background goroutines to exit before the DB is closed.
	t.Cleanup(func() { _ = s.WaitForBackground(context.Background()) })

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, sessions
}

// wsURL builds a WebSocket URL for the given test server path+query.
func wsURL(t *testing.T, ts *httptest.Server, path, rawQuery string) string {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	u.Scheme = "ws"
	u.Path = path
	u.RawQuery = rawQuery
	return u.String()
}

// readWSMessage blocks until a text message of the expected type arrives.
// Intermediate messages (e.g. "typing" indicators) are skipped.
func readWSMessage(t *testing.T, conn *websocket.Conn, expectedType string, timeout time.Duration) *WsMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket message: %v", err)
		}
		if msgType != websocket.TextMessage {
			continue
		}
		var wm WsMessage
		if err := json.Unmarshal(data, &wm); err != nil {
			continue
		}
		if wm.Type == expectedType {
			return &wm
		}
	}
}

// TestChatRequiresSession asserts /api/chat is session-gated: an anonymous
// request is rejected with 401 before the WebSocket upgrade.
func TestChatRequiresSession(t *testing.T) {
	ts, _ := newChatSecurityServer(t)

	resp, err := http.Get(ts.URL + "/api/chat")
	if err != nil {
		t.Fatalf("GET /api/chat: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for anonymous /api/chat, got %d", resp.StatusCode)
	}
}

// TestChatUnknownUserRejected asserts a session whose user ID does not map to
// any configured user is rejected with 403 before the WebSocket upgrade.
func TestChatUnknownUserRejected(t *testing.T) {
	ts, sessions := newChatSecurityServer(t)

	sessID, err := sessions.Create(context.Background(), 99, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create: %v", err)
	}

	_, resp, err := websocket.DefaultDialer.Dial(wsURL(t, ts, "/api/chat", ""),
		http.Header{"Cookie": []string{"famclaw_session=" + sessID}})
	if err == nil {
		t.Fatal("expected dial to fail for out-of-range session user ID")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for unknown user session, got %v", resp)
	}
}

// TestChatIgnoresUserQueryParam asserts the ?user= query parameter no longer
// determines chat identity. A child session that names the parent in ?user=
// still resolves to the session's own user: policy is evaluated against the
// child, so the social-media request is gated instead of allowed.
func TestChatIgnoresUserQueryParam(t *testing.T) {
	ts, sessions := newChatSecurityServer(t)

	// emma is config.Users[1] → synthesised session user ID 2.
	sessID, err := sessions.Create(context.Background(), 2, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create: %v", err)
	}

	// Attacker tries to claim the parent identity via the query parameter.
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(t, ts, "/api/chat", "user=parent"),
		http.Header{"Cookie": []string{"famclaw_session=" + sessID}})
	if err != nil {
		t.Fatalf("dial: %v (resp=%v)", err, resp)
	}
	defer conn.Close()

	if err := conn.WriteJSON(WsMessage{Type: "chat", Payload: []byte(`{"text":"can I use instagram and tiktok"}`)}); err != nil {
		t.Fatalf("write chat message: %v", err)
	}

	respMsg := readWSMessage(t, conn, "message", 5*time.Second)
	var respData map[string]interface{}
	if err := json.Unmarshal(respMsg.Payload, &respData); err != nil {
		t.Fatalf("unmarshal response payload: %v", err)
	}
	policyAction, _ := respData["policy_action"].(string)
	// If ?user=parent were honoured, policy_action would be "allow" (the parent
	// role always passes). Emma is a child: social media is gated for her.
	if policyAction == "allow" {
		t.Errorf("expected social-media request to be gated for the child session, got allow — ?user= param is still driving identity")
	}
}

// TestChatParentOverrideUser asserts the documented preview path: a
// parent session may chat as another household member via ?user=<name>.
// The response is gated by the *child's* policy (social media blocked for
// under-13), proving the override actually switched identity.
func TestChatParentOverrideUser(t *testing.T) {
	ts, sessions := newChatSecurityServer(t)

	// parent is config.Users[0] → synthesised session user ID 1.
	sessID, err := sessions.Create(context.Background(), 1, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create: %v", err)
	}

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(t, ts, "/api/chat", "user=emma"),
		http.Header{"Cookie": []string{"famclaw_session=" + sessID}})
	if err != nil {
		t.Fatalf("dial: %v (resp=%v)", err, resp)
	}
	defer conn.Close()

	if err := conn.WriteJSON(WsMessage{Type: "chat", Payload: []byte(`{"text":"can I use instagram and tiktok"}`)}); err != nil {
		t.Fatalf("write chat message: %v", err)
	}

	respMsg := readWSMessage(t, conn, "message", 5*time.Second)
	var respData map[string]interface{}
	if err := json.Unmarshal(respMsg.Payload, &respData); err != nil {
		t.Fatalf("unmarshal response payload: %v", err)
	}
	policyAction, _ := respData["policy_action"].(string)
	// As emma (child, age_8_12) social media is gated; as the parent it
	// would be "allow". request_approval proves the override switched to emma.
	if policyAction != "request_approval" {
		t.Errorf("expected policy_action request_approval (child policy applied), got %q", policyAction)
	}
}

// TestChatAllowedOrigin is a table-driven unit test of the WebSocket origin
// policy: no Origin (non-browser) is allowed, a same-origin browser is allowed,
// any foreign origin is rejected.
func TestChatAllowedOrigin(t *testing.T) {
	s := &Server{cfg: &config.Config{}}

	tests := []struct {
		name   string
		origin string
		host   string
		set    bool
		want   bool
	}{
		{name: "no origin header", origin: "", host: "127.0.0.1:8080", set: false, want: true},
		{name: "same origin", origin: "http://127.0.0.1:8080", host: "127.0.0.1:8080", set: true, want: true},
		{name: "foreign host", origin: "http://evil.example.com", host: "127.0.0.1:8080", set: true, want: false},
		{name: "same host different port", origin: "http://127.0.0.1:9999", host: "127.0.0.1:8080", set: true, want: false},
		{name: "scheme mismatch", origin: "https://127.0.0.1:8080", host: "127.0.0.1:8080", set: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/api/chat", nil)
			if tt.set {
				r.Header.Set("Origin", tt.origin)
			}
			if got := s.allowedOrigin(r); got != tt.want {
				t.Errorf("allowedOrigin(origin=%q, host=%q) = %v, want %v", tt.origin, tt.host, got, tt.want)
			}
		})
	}
}
