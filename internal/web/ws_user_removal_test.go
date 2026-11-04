package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/gorilla/websocket"
)

// TestServerWSLiveUserRemoval is the #429 regression test for the nil deref of
// resolveUserRole's return in handleChat: a user who is present in the config
// while a live WebSocket session is open, then removed from the config
// (hot-reload), must have the session terminated fail-closed — no panic, no
// ghost session — and a specific, observable close. The control case asserts
// the session keeps working while the user remains in the config.
//
// No live inference is involved: the probe message is policy-gated
// (request_approval) before any LLM call.
func TestServerWSLiveUserRemoval(t *testing.T) {
	tests := []struct {
		name            string
		removedDuring   bool // emma is removed from cfg while her WS session is live
		wantClosed      bool // after the second chat message, the conn must be dead
		wantErrorSignal bool // the guard's specific "error" WS message must arrive
	}{
		{name: "user still in config: session continues", removedDuring: false, wantClosed: false, wantErrorSignal: false},
		{name: "user removed from config: session terminated fail-closed", removedDuring: true, wantClosed: true, wantErrorSignal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// --- server fixture (same shape as TestServerWebChatRoleOverrideIntegration) ---
			tmpDir := t.TempDir()
			db, err := store.Open(filepath.Join(tmpDir, "test.db"))
			if err != nil {
				t.Fatalf("store.Open: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			sessions := store.NewSessionStore(db.SQL())

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
				clf:        classifier.New(),
				sessions:   sessions,
				notifier:   notify.NewMultiNotifier(cfg, identStore, func(ctx context.Context, gw, chatID, text string) error { return nil }),
				cfgMu:      sync.RWMutex{},
				clients:    make(map[*websocket.Conn]*wsClient),
			}
			s.upgrader = websocket.Upgrader{CheckOrigin: s.allowedOrigin}

			t.Cleanup(func() { _ = s.WaitForBackground(context.Background()) })

			ts := httptest.NewServer(s.Handler())
			t.Cleanup(ts.Close)

			// --- open emma's session ---
			ctx := context.Background()
			sessID, err := sessions.Create(ctx, "emma", "127.0.0.1", "test")
			if err != nil {
				t.Fatalf("sessions.Create: %v", err)
			}

			u, err := url.Parse(ts.URL)
			if err != nil {
				t.Fatalf("url.Parse: %v", err)
			}
			u.Scheme = "ws"
			u.Path = "/api/chat"
			conn, _, err := websocket.DefaultDialer.Dial(u.String(), http.Header{"Cookie": []string{"famclaw_session=" + sessID}})
			if err != nil {
				t.Fatalf("dial: %v", err)
			}

			var readerDone sync.WaitGroup
			readerDone.Add(1)
			msgChan := make(chan struct {
				data []byte
				err  error
			}, 10)
			go func() {
				defer readerDone.Done()
				for {
					_, data, err := conn.ReadMessage()
					if err != nil {
						select {
						case msgChan <- struct {
							data []byte
							err  error
						}{nil, err}:
						default:
						}
						return
					}
					msgChan <- struct {
						data []byte
						err  error
					}{data, nil}
				}
			}()
			t.Cleanup(func() {
				_ = conn.Close()
				readerDone.Wait()
			})

			// waitForWSMessage skips non-text frames and returns the first
			// WsMessage of the expected type, or a read error/close on EOF.
			waitForWSMessage := func(expectedType string, timeout time.Duration) (*WsMessage, error) {
				deadline := time.After(timeout)
				for {
					select {
					case m := <-msgChan:
						if m.err != nil {
							return nil, m.err
						}
						var wm WsMessage
						if err := json.Unmarshal(m.data, &wm); err != nil {
							continue
						}
						if wm.Type == expectedType {
							return &wm, nil
						}
					case <-deadline:
						return nil, fmt.Errorf("timeout waiting for WS message type %s", expectedType)
					}
				}
			}

			// --- step 1: prove the session works while emma is in the config ---
			chatData, err := json.Marshal(WsMessage{
				Type:    "chat",
				Payload: []byte(`{"text":"can I use instagram and tiktok"}`),
			})
			if err != nil {
				t.Fatalf("marshal chat msg: %v", err)
			}
			if err := conn.WriteMessage(websocket.TextMessage, chatData); err != nil {
				t.Fatalf("write chat message: %v", err)
			}
			resp, err := waitForWSMessage("message", 5*time.Second)
			if err != nil {
				t.Fatalf("first response (session must be live): %v", err)
			}
			var first map[string]interface{}
			if err := json.Unmarshal(resp.Payload, &first); err != nil {
				t.Fatalf("unmarshal first response: %v", err)
			}
			if first["policy_action"] != "request_approval" {
				t.Fatalf("first response policy_action = %v, want request_approval", first["policy_action"])
			}

			// --- step 2: optionally remove emma from the config while live ---
			if tt.removedDuring {
				s.cfgMu.Lock()
				kept := cfg.Users[:0]
				for _, u := range cfg.Users {
					if u.Name != "emma" {
						kept = append(kept, u)
					}
				}
				cfg.Users = kept
				s.cfgMu.Unlock()
				if got := cfg.GetUser("emma"); got != nil {
					t.Fatalf("fixture broken: emma still resolves after removal")
				}
			}

			// --- step 3: send another chat message and observe the guard ---
			if err := conn.WriteMessage(websocket.TextMessage, chatData); err != nil {
				t.Fatalf("write second chat message: %v", err)
			}

			if tt.wantErrorSignal {
				guard, err := waitForWSMessage("error", 5*time.Second)
				if err != nil {
					t.Fatalf("guard error message not delivered: %v", err)
				}
				var payload map[string]string
				if err := json.Unmarshal(guard.Payload, &payload); err != nil {
					t.Fatalf("unmarshal guard payload: %v", err)
				}
				if !strings.Contains(payload["error"], "no longer exists in this household") {
					t.Errorf("guard error = %q, want the specific removed-account signal", payload["error"])
				}
			}

			if tt.wantClosed {
				// Terminate behavior: the reader goroutine must observe the
				// connection die (server-side conn.Close on guard return), not
				// merely survive a nil deref.
				deadline := time.After(5 * time.Second)
				closed := false
				for !closed {
					select {
					case m, ok := <-msgChan:
						if !ok || m.err != nil {
							closed = true
						}
					case <-deadline:
					}
				}
				if !closed {
					t.Fatalf("connection still open 5s after user removal — session was not terminated")
				}
			} else {
				// Control: the second message must be answered again — session
				// alive. The policy action is request_approval, or "pending"
				// when the first message's approval is still outstanding
				// (dedup); either proves the session survived.
				resp2, err := waitForWSMessage("message", 5*time.Second)
				if err != nil {
					t.Fatalf("second response (session must stay live): %v", err)
				}
				var second map[string]interface{}
				if err := json.Unmarshal(resp2.Payload, &second); err != nil {
					t.Fatalf("unmarshal second response: %v", err)
				}
				action, _ := second["policy_action"].(string)
				if action != "request_approval" && action != "pending" {
					t.Fatalf("second response policy_action = %v, want request_approval or pending", second["policy_action"])
				}
			}
		})
	}
}
