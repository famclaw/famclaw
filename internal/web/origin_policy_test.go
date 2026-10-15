package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/famclaw/famclaw/internal/config"
)

// configWithPolicy builds the minimal config allowedOrigin reads for the
// origin policy (wsOriginPolicy takes the config lock, so a zero-value
// mutex is required).
func configWithPolicy(policy string) config.Config {
	return config.Config{Server: config.ServerConfig{WSOriginPolicy: policy}}
}

// TestAllowedOriginPolicy extends the origin matrix from
// TestChatAllowedOrigin with the policy dimension (design-note T6–T9):
// a present Origin must equal the server's own origin in BOTH modes; a
// missing Origin is allowed under "same-origin" (non-browser clients: curl,
// scripts, native apps — the SameSite=Strict cookie is the cross-site gate)
// and rejected under "strict" (browser-handshake-only).
func TestAllowedOriginPolicy(t *testing.T) {
	tests := []struct {
		policy string
		name   string
		origin string
		host   string
		set    bool
		want   bool
	}{
		{policy: "same-origin", name: "no origin header", origin: "", host: "127.0.0.1:8080", set: false, want: true},
		{policy: "same-origin", name: "same origin", origin: "http://127.0.0.1:8080", host: "127.0.0.1:8080", set: true, want: true},
		{policy: "same-origin", name: "foreign host", origin: "http://evil.example.com", host: "127.0.0.1:8080", set: true, want: false},
		{policy: "same-origin", name: "same host different port", origin: "http://127.0.0.1:9999", host: "127.0.0.1:8080", set: true, want: false},
		{policy: "same-origin", name: "scheme mismatch", origin: "https://127.0.0.1:8080", host: "127.0.0.1:8080", set: true, want: false},
		{policy: "strict", name: "no origin header rejected", origin: "", host: "127.0.0.1:8080", set: false, want: false},
		{policy: "strict", name: "same origin still allowed", origin: "http://127.0.0.1:8080", host: "127.0.0.1:8080", set: true, want: true},
		{policy: "strict", name: "foreign host rejected", origin: "http://evil.example.com", host: "127.0.0.1:8080", set: true, want: false},
		{policy: "strict", name: "scheme mismatch rejected", origin: "https://127.0.0.1:8080", host: "127.0.0.1:8080", set: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.policy+" "+tt.name, func(t *testing.T) {
			cfg := configWithPolicy(tt.policy)
			s := &Server{cfg: &cfg}
			r := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/api/chat", nil)
			if tt.set {
				r.Header.Set("Origin", tt.origin)
			}
			if got := s.allowedOrigin(r); got != tt.want {
				t.Errorf("allowedOrigin(policy=%q, origin=%q, host=%q) = %v, want %v", tt.policy, tt.origin, tt.host, got, tt.want)
			}
		})
	}
}

// TestOriginPolicyUpgrader exercises the REAL websocket.Upgrader.Upgrade path
// (design-note risk 3: the tests must drive the upgrader, not just the
// predicate): cross-origin handshakes are refused with the upgrader's
// default 403, missing-Origin handshakes are allowed in "same-origin" mode
// and refused in "strict" mode, and a valid session cookie does not bypass
// the origin check in "strict" mode (the origin gate runs on the upgrade
// regardless of the session gate's verdict).
func TestOriginPolicyUpgrader(t *testing.T) {
	tests := []struct {
		policy     string
		name       string
		origin     string
		wantOK     bool
		wantStatus int
	}{
		{policy: "same-origin", name: "missing origin: non-browser client", origin: "", wantOK: true},
		{policy: "same-origin", name: "same origin browser", origin: "{{SELF}}", wantOK: true},
		{policy: "same-origin", name: "cross-origin page-hosting", origin: "http://evil.example.com", wantOK: false, wantStatus: http.StatusForbidden},
		{policy: "strict", name: "missing origin rejected", origin: "", wantOK: false, wantStatus: http.StatusForbidden},
		{policy: "strict", name: "cross-origin + valid cookie rejected", origin: "http://evil.example.com", wantOK: false, wantStatus: http.StatusForbidden},
		{policy: "strict", name: "same origin browser allowed", origin: "{{SELF}}", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.policy+" "+tt.name, func(t *testing.T) {
			ts, deps := newJoinSecurityServer(t, tt.policy)

			// A valid CHILD session (config index 1 + 1) so the request gets
			// through the session gate and reaches the origin check in the
			// upgrader — the cookie must never be able to bypass it.
			sid, err := deps.sessions.Create(context.Background(), 2, "127.0.0.1", "test")
			if err != nil {
				t.Fatalf("sessions.Create: %v", err)
			}

			u, err := url.Parse(ts.URL + "/api/chat")
			if err != nil {
				t.Fatalf("url.Parse: %v", err)
			}
			u.Scheme = "ws"
			header := http.Header{"Cookie": []string{"famclaw_session=" + sid}}
			origin := tt.origin
			if origin == "{{SELF}}" {
				origin = ts.URL // the test server's own http origin
			}
			if origin != "" {
				header.Set("Origin", origin)
			}

			conn, resp, err := websocket.DefaultDialer.Dial(u.String(), header)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("dial failed: %v (resp=%v)", err, resp)
				}
				defer conn.Close()
				return
			}
			if err == nil {
				conn.Close()
				t.Fatalf("dial succeeded, want rejection")
			}
			if resp == nil || resp.StatusCode != tt.wantStatus {
				t.Errorf("rejection status = %v, want %d", resp, tt.wantStatus)
			}
		})
	}
}
