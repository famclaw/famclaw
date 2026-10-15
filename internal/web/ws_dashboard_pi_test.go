package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestDashboardUpdatePIIChildScoping asserts the WS dashboard_update frame is
// scoped by the *session* role (before any ?user= preview override): a child
// session still receives the household pending-approval badge (the same count
// the SSE stream already sends to every session), but never the
// unknown-accounts PII list (display name, gateway, external ID) — that list
// is parent management surface, as is /api/unknown-accounts (see the
// route-table design note). A parent session receives the list.
func TestDashboardUpdatePIIChildScoping(t *testing.T) {
	ts, deps := newJoinSecurityServer(t, "")
	s := deps.s

	// Seed an unknown account: the PII that must never reach a child socket.
	if err := s.identStore.RecordUnknown(context.Background(), "telegram", "ext-alice-9", "Alice"); err != nil {
		t.Fatalf("RecordUnknown: %v", err)
	}

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	u.Scheme = "ws"
	u.Path = "/api/chat"
	wsURL := u.String()

	ctx := context.Background()

	// Child session: config.Users[1] → synthesised session user ID 2.
	childSess, err := deps.sessions.Create(ctx, 2, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create (child): %v", err)
	}
	childConn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Cookie": []string{"famclaw_session=" + childSess}})
	if err != nil {
		t.Fatalf("dial (child): %v", err)
	}
	defer childConn.Close()

	// Parent session: config.Users[0] → synthesised session user ID 1.
	parentSess, err := deps.sessions.Create(ctx, 1, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create (parent): %v", err)
	}
	parentConn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Cookie": []string{"famclaw_session=" + parentSess}})
	if err != nil {
		t.Fatalf("dial (parent): %v", err)
	}
	defer parentConn.Close()

	// Sanity: both sockets registered with the expected session role. The
	// server map is keyed by the SERVER-side conn from Upgrade, so look the
	// clients up by userName instead of the client-side conn from Dial.
	s.clientsMu.RLock()
	var childClient, parentClient *wsClient
	for _, c := range s.clients {
		switch c.userName {
		case "emma":
			childClient = c
		case "parent":
			parentClient = c
		}
	}
	s.clientsMu.RUnlock()
	if childClient == nil || parentClient == nil {
		t.Fatalf("expected both sockets registered: child found=%v, parent found=%v", childClient != nil, parentClient != nil)
	}
	if childClient.parent {
		t.Fatal("child socket registered as parent")
	}
	if !parentClient.parent {
		t.Fatal("parent socket not registered as parent")
	}

	// Trigger the broadcast the way the production paths do (chat message,
	// approval decision): tracked on bgWG, and wait for it to finish before
	// reading so both sockets have their frame on the wire.
	done := make(chan struct{})
	s.bgWG.Add(1)
	go func() {
		s.broadcastDashboardUpdate(context.Background())
		close(done)
	}()

	// readDashboardUpdate reads frames until it finds the dashboard_update
	// frame (skipping the initial chat-session frame) and returns its payload.
	readDashboardUpdate := func(t *testing.T, conn *websocket.Conn, who string) map[string]any {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for i := 0; ; i++ {
			if i > 10 {
				t.Fatalf("%s: no dashboard_update frame within 10 frames", who)
			}
			_, raw, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("%s: ReadMessage: %v", who, err)
			}
			var m struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("%s: unmarshal frame: %v", who, err)
			}
			if m.Type != "dashboard_update" {
				continue
			}
			var p map[string]any
			if err := json.Unmarshal(m.Payload, &p); err != nil {
				t.Fatalf("%s: unmarshal payload: %v", who, err)
			}
			return p
		}
	}

	// Wait for the broadcast to have written both frames before reading.
	<-done

	childPayload := readDashboardUpdate(t, childConn, "child")
	// The badge count must still reach the child (frame is not dropped).
	if _, has := childPayload["pending_count"]; !has {
		t.Errorf("child dashboard_update missing pending_count: %v", childPayload)
	}
	// ... but never the unknown-accounts PII (omitted or null, never a list).
	if v, has := childPayload["unknown_accounts"]; has && v != nil {
		t.Errorf("child dashboard_update leaked unknown_accounts PII: %v", v)
	}

	parentPayload := readDashboardUpdate(t, parentConn, "parent")
	unknownList, _ := parentPayload["unknown_accounts"].([]any)
	found := false
	for _, item := range unknownList {
		if m, ok := item.(map[string]any); ok {
			if name, _ := m["display_name"].(string); name == "Alice" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("parent dashboard_update did not carry the seeded unknown account: %v", parentPayload)
	}
}
