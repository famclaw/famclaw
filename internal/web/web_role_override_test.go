package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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

// TestServerResolveUserRoleFromDB verifies that resolveUserRole returns the
// overridden role/age when a DB-persisted override exists, and the config
// role/age when no override exists.  This exercises the exact code path
// handleChat uses to build adjustedUser before creating the agent.
func TestServerResolveUserRoleFromDB(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := store.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

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
	clf := classifier.New()

	s := &Server{
		cfg:        cfg,
		db:         db,
		identStore: identStore,
		evaluator:  ev,
		clf:        clf,
		cfgMu:      sync.RWMutex{},
	}
	ctx := context.Background()

	// --- No override: resolveUserRole returns config values ---
	user := s.resolveUserRole(ctx, "emma")
	if user == nil {
		t.Fatal("resolveUserRole returned nil for emma")
	}
	if user.Role != "child" {
		t.Errorf("no override: Role = %q, want %q", user.Role, "child")
	}
	if user.AgeGroup != "age_8_12" {
		t.Errorf("no override: AgeGroup = %q, want %q", user.AgeGroup, "age_8_12")
	}

	// Verify emma (age_8_12) would request_approval for social media.
	decision, err := ev.Evaluate(ctx, policy.Input{
		User:  policy.UserInput{Role: user.Role, AgeGroup: user.AgeGroup, Name: "emma"},
		Query: policy.QueryInput{Category: "social_media", Text: "can I use instagram and tiktok"},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision.Action != "request_approval" {
		t.Errorf("no override: PolicyAction = %q, want request_approval", decision.Action)
	}

	// --- Set override: emma -> under_8 ---
	err = db.SetRoleOverride(ctx, "emma", "child", "under_8", "parent")
	if err != nil {
		t.Fatalf("SetRoleOverride: %v", err)
	}
	defer db.SetRoleOverride(ctx, "emma", "", "", "parent")

	// Verify resolveUserRole picks up the override.
	user = s.resolveUserRole(ctx, "emma")
	if user == nil {
		t.Fatal("resolveUserRole returned nil for emma after override")
	}
	if user.Role != "child" {
		t.Errorf("with override: Role = %q, want %q", user.Role, "child")
	}
	if user.AgeGroup != "under_8" {
		t.Errorf("with override: AgeGroup = %q, want %q", user.AgeGroup, "under_8")
	}

	// The resolved user must differ from config (proves the copy-and-override path was taken).
	configUser := cfg.GetUser("emma")
	if user.Role == configUser.Role && user.AgeGroup == configUser.AgeGroup {
		t.Error("resolveUserRole returned config values despite an override being set")
	}

	// Verify emma (under_8) is blocked from social media.
	decision, err = ev.Evaluate(ctx, policy.Input{
		User:  policy.UserInput{Role: user.Role, AgeGroup: user.AgeGroup, Name: "emma"},
		Query: policy.QueryInput{Category: "social_media", Text: "can I use instagram and tiktok"},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision.Action != "block" {
		t.Errorf("with under_8 override: PolicyAction = %q, want block", decision.Action)
	}
	if decision.Reason == "" {
		t.Error("expected a block reason in the decision, got empty reason")
	}
}

// TestServerWebChatRoleOverrideIntegration verifies the full WebSocket flow:
// that DB-persisted role/age overrides (set via set_user_role) are consulted
// during web-chat policy evaluation, and that agent recreation picks up
// changed overrides in real time.
func TestServerWebChatRoleOverrideIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := store.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

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
	// Link emma to a telegram external ID so the identity store resolves her.
	identStore.LinkAccount("emma", "telegram", "ro-emma-123")

	s := &Server{
		cfg:        cfg,
		db:         db,
		identStore: identStore,
		evaluator:  ev,
		clf:        clf,
		notifier:   &notify.MultiNotifier{},
		cfgMu:      sync.RWMutex{},
		clients:    make(map[*websocket.Conn]*wsClient),
	}

	// Start the test server.
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Prepare WebSocket URL.
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	u.Scheme = "ws"
	u.Path = "/api/chat"
	q := u.Query()
	q.Set("user", "emma")
	u.RawQuery = q.Encode()
	wsURL := u.String()

	// Dial the WebSocket connection.
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Channel to receive incoming WebSocket messages.
	msgChan := make(chan struct {
		msgType int
		data    []byte
		err     error
	}, 10)

	// Goroutine to read WebSocket messages.
	go func() {
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					// Ignore unexpected close error.
				}
				msgChan <- struct {
					msgType int
					data    []byte
					err     error
				}{msgType: -1, data: nil, err: err}
				return
			}
			msgChan <- struct {
				msgType int
				data    []byte
				err     error
			}{msgType: msgType, data: data, err: nil}
		}
	}()

	// Helper to wait for a message of a given type (text) and optionally check its content.
	waitForMessage := func(expectedType string, timeout time.Duration) (*WsMessage, error) {
		ticker := time.NewTicker(timeout)
		defer ticker.Stop()
		for {
			select {
			case msg := <-msgChan:
				if msg.err != nil {
					return nil, msg.err
				}
				if msg.msgType == websocket.CloseMessage {
					return nil, errors.New("websocket closed")
				}
				if msg.msgType != websocket.TextMessage {
					continue
				}
				var wm WsMessage
				if err := json.Unmarshal(msg.data, &wm); err != nil {
					continue
				}
				if wm.Type == expectedType {
					return &wm, nil
				}
				// Otherwise, continue waiting.
			case <-ticker.C:
				return nil, fmt.Errorf("timeout waiting for message type %s", expectedType)
			}
		}
	}

	ctx := context.Background()

	// Step 1: Set the role override for emma to child/under_8 (simulate parent calling set_user_role).
	err = db.SetRoleOverride(ctx, "emma", "child", "under_8", "parent")
	if err != nil {
		t.Fatalf("SetRoleOverride: %v", err)
	}
	// Clean up after the test.
	defer db.SetRoleOverride(ctx, "emma", "", "", "parent")

	// Step 2: Send a chat message from emma and expect it to be blocked (because under_8 cannot use social_media).
	chatMsg := WsMessage{
		Type:    "chat",
		Payload: []byte(`{"text":"can I use instagram and tiktok"}`),
	}
	chatData, err := json.Marshal(chatMsg)
	if err != nil {
		t.Fatalf("marshal chat msg: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, chatData); err != nil {
		t.Fatalf("write chat message: %v", err)
	}

	// Wait for the response message (which should be of type "message" with policy_action "block").
	respMsg, err := waitForMessage("message", 5*time.Second)
	if err != nil {
		t.Fatalf("waiting for response: %v", err)
	}
	// Parse the payload of the response message to get the policy_action.
	var respData map[string]interface{}
	if err := json.Unmarshal(respMsg.Payload, &respData); err != nil {
		t.Fatalf("unmarshal response payload: %v", err)
	}
	policyAction, ok := respData["policy_action"].(string)
	if !ok {
		t.Fatalf("policy_action not found or not string in response: %v", respData)
	}
	if policyAction != "block" {
		t.Errorf("expected policy_action block, got %q", policyAction)
	}

	// Step 3: Remove the override (set to empty) and send another chat message.
	// This should now be request_approval because emma is age_8_12.
	err = db.SetRoleOverride(ctx, "emma", "", "", "parent")
	if err != nil {
		t.Fatalf("SetRoleOverride (clear): %v", err)
	}
	// Verify the override is cleared.
	role, ageGroup, err := db.GetRoleOverride(ctx, "emma")
	if err != nil {
		t.Fatalf("GetRoleOverride after clear: %v", err)
	}
	if role != "" || ageGroup != "" {
		t.Fatalf("expected override cleared, got %q/%q", role, ageGroup)
	}

	// Send the same chat message again.
	if err := conn.WriteMessage(websocket.TextMessage, chatData); err != nil {
		t.Fatalf("write chat message (second): %v", err)
	}

	// Wait for the response message (should be of type "message" with policy_action "request_approval").
	respMsg2, err := waitForMessage("message", 5*time.Second)
	if err != nil {
		t.Fatalf("waiting for second response: %v", err)
	}
	var respData2 map[string]interface{}
	if err := json.Unmarshal(respMsg2.Payload, &respData2); err != nil {
		t.Fatalf("unmarshal second response payload: %v", err)
	}
	policyAction2, ok := respData2["policy_action"].(string)
	if !ok {
		t.Fatalf("policy_action not found or not string in second response: %v", respData2)
	}
	if policyAction2 != "request_approval" {
		t.Errorf("expected policy_action request_approval, got %q", policyAction2)
	}
}
