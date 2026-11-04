package web

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/famclaw/famclaw/internal/agent"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// TestRequestApprovalDetachesContext verifies that requestApproval's DB
// upsert runs under a DETACHED context — context.WithoutCancel(ctx) — so the
// approval row persists even when the chat request context is already
// cancelled (the user sent a gated message and closed the tab before the
// background goroutine executed the write).
//
// Probe: database/sql + modernc.org/sqlite fail a query whose context is
// already cancelled before execution (verified against store.DB directly in
// the predecessor of this test). So a live ctx inserts the row, and — crucial
// for this round — a pre-cancelled ctx must ALSO insert the row: the upsert
// survives cancellation because requestApproval detaches it. If
// requestApproval regressed to threading the raw request context, the
// cancelled-ctx case would abort the write and leave no row. Offline: no
// LLM, no network.
func TestRequestApprovalDetachesContext(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := store.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s := &Server{
		cfg: &config.Config{Server: config.ServerConfig{Secret: "test-secret", MDNSName: "famclaw"}},
		db:  db,
	}
	user := &config.UserConfig{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"}

	countRow := func(id string) int {
		t.Helper()
		var n int
		if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM approvals WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatalf("count approvals: %v", err)
		}
		return n
	}

	t.Run("live context reaches the upsert", func(t *testing.T) {
		id := agent.ApprovalID(user.Name, "social")
		s.requestApproval(context.Background(), user, "social", "can I use instagram")
		if n := countRow(id); n != 1 {
			t.Fatalf("live-ctx upsert: expected approval row %q, got count=%d", id, n)
		}
	})

	t.Run("cancelled context still writes the row (detached)", func(t *testing.T) {
		id := agent.ApprovalID(user.Name, "drugs")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s.requestApproval(ctx, user, "drugs", "tell me about drugs")
		if n := countRow(id); n != 1 {
			t.Fatalf("cancelled-ctx upsert must still write the row — requestApproval is not detaching the DB write from the request context (expected context.WithoutCancel)")
		}
	})
}

// TestChatApprovalPathUpsertsPendingRow drives the full HTTP handler chain
// offline: a child's gated chat message (social media → OPA decision
// request_approval; the policy block returns before any LLM call) must
// surface a pending approval row for the child, created by
// handleChat → spawnBackground → requestApproval(r.Context()) →
// s.db.UpsertApproval(context.WithoutCancel(ctx)). It complements
// TestRequestApprovalDetachesContext, which proves the upsert survives a
// cancelled request context.
func TestChatApprovalPathUpsertsPendingRow(t *testing.T) {
	ts, sessions := newChatSecurityServer(t)

	// Sessions bind to the stable config principal (name), not a config-array
	// index (Terra F7, main #429).
	childSess, err := sessions.Create(context.Background(), "emma", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create (child): %v", err)
	}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(t, ts, "/api/chat", ""),
		http.Header{"Cookie": []string{"famclaw_session=" + childSess}})
	if err != nil {
		t.Fatalf("dial (child): %v (resp=%v)", err, resp)
	}
	defer conn.Close()

	if err := conn.WriteJSON(WsMessage{Type: "chat", Payload: []byte(`{"text":"can I use instagram and tiktok"}`)}); err != nil {
		t.Fatalf("write chat message: %v", err)
	}

	respMsg := readWSMessage(t, conn, "message", 10*time.Second)
	var respData map[string]interface{}
	if err := json.Unmarshal(respMsg.Payload, &respData); err != nil {
		t.Fatalf("unmarshal response payload: %v", err)
	}
	if policyAction, _ := respData["policy_action"].(string); policyAction != "request_approval" {
		t.Fatalf("expected policy_action request_approval, got %q", policyAction)
	}

	// The upsert runs in a background goroutine spawned by handleChat — poll
	// the parent management surface for the pending row.
	parentSess, err := sessions.Create(context.Background(), "parent", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create (parent): %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		found := false
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/approvals", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.AddCookie(&http.Cookie{Name: "famclaw_session", Value: parentSess})
		httpResp, err := http.DefaultClient.Do(req)
		if err == nil && httpResp.StatusCode == http.StatusOK {
			var approvals []struct {
				UserName string `json:"user_name"`
				Category string `json:"category"`
				Status   string `json:"status"`
			}
			if json.NewDecoder(httpResp.Body).Decode(&approvals) == nil {
				for _, a := range approvals {
					if a.UserName == "emma" && a.Status == "pending" {
						t.Logf("pending approval row for emma: category=%q", a.Category)
						found = true
					}
				}
			}
			httpResp.Body.Close()
		} else {
			if httpResp != nil {
				httpResp.Body.Close()
			}
		}
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for emma's pending approval row on /api/approvals")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
