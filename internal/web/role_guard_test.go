package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// adminRoutes are the routes mounted behind s.protectParent (design note §3):
// a valid session is not enough — the session's user must have the BASE
// config role "parent". This table is the tripwire for D5: any route
// accidentally left on plain s.protect keeps a child-session hole, and this
// test catches it (design-note T16).
var adminRoutes = []struct {
	method string
	path   string
}{
	{http.MethodGet, "/api/users"},
	{http.MethodGet, "/api/approvals"},
	{http.MethodPost, "/api/approvals/decide"},
	{http.MethodGet, "/api/skills"},
	{http.MethodPost, "/api/skills/install"},
	{http.MethodPost, "/api/skills/remove"},
	{http.MethodGet, "/api/mcp"},
	{http.MethodPost, "/api/mcp/add"},
	{http.MethodPost, "/api/mcp/remove"},
	{http.MethodGet, "/api/unknown-accounts"},
	{http.MethodPost, "/api/unknown-accounts/link"},
	{http.MethodPost, "/api/unknown-accounts/dismiss"},
	{http.MethodGet, "/api/settings"},
	{http.MethodPost, "/api/settings"},
	{http.MethodPost, "/api/users/emma/join-code"},
	// Setup-wizard probes: pre-PIN they are public, post-PIN (the fixture's
	// state) they are parent-only via conditionalProtect → protectParent.
	{http.MethodPost, "/api/setup/test-telegram"},
	{http.MethodPost, "/api/setup/test-discord"},
}

// TestChildSessionForbiddenOnAdminRoutes (design-note T16, part 1): a valid
// CHILD session gets 403 on every admin route — the role gate runs before
// the handler, so the request cannot reach any management surface.
func TestChildSessionForbiddenOnAdminRoutes(t *testing.T) {
	ts, deps := newJoinSecurityServer(t, "")

	// emma is config.Users[1] → synthesised session user ID 2.
	sessID, err := deps.sessions.Create(context.Background(), 2, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create: %v", err)
	}

	for _, rt := range adminRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			req, err := http.NewRequest(rt.method, ts.URL+rt.path, strings.NewReader("{}"))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(&http.Cookie{Name: "famclaw_session", Value: sessID})
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", rt.method, rt.path, err)
			}
			defer resp.Body.Close()
			drain(resp.Body)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s: child session status = %d, want 403", rt.method, rt.path, resp.StatusCode)
			}
		})
	}
}

// TestParentSessionAllowedOnAdminRoutes (design-note T16, part 2): the SAME
// routes for a valid PARENT session never return 401/403 — the role gate
// must not block legitimate management traffic (the specific status varies
// by handler and body, which is out of scope for the tripwire).
func TestParentSessionAllowedOnAdminRoutes(t *testing.T) {
	ts, deps := newJoinSecurityServer(t, "")

	// parent is config.Users[0] → synthesised session user ID 1.
	sessID, err := deps.sessions.Create(context.Background(), 1, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create: %v", err)
	}

	for _, rt := range adminRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			req, err := http.NewRequest(rt.method, ts.URL+rt.path, strings.NewReader(`{"action":"issue"}`))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(&http.Cookie{Name: "famclaw_session", Value: sessID})
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", rt.method, rt.path, err)
			}
			defer resp.Body.Close()
			drain(resp.Body)
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				t.Errorf("%s %s: parent session status = %d, want a handler response (not 401/403)", rt.method, rt.path, resp.StatusCode)
			}
		})
	}
}

// TestAdminRoutesRequireSession (design-note T2): anonymous requests to the
// admin surface get 401 from the session gate — the role gate is the second
// door, not the first.
func TestAdminRoutesRequireSession(t *testing.T) {
	ts, _ := newJoinSecurityServer(t, "")

	for _, rt := range adminRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			req, err := http.NewRequest(rt.method, ts.URL+rt.path, strings.NewReader("{}"))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", rt.method, rt.path, err)
			}
			defer resp.Body.Close()
			drain(resp.Body)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s: anonymous status = %d, want 401", rt.method, rt.path, resp.StatusCode)
			}
		})
	}
}

// TestUsersPublicListsOnlyChildren checks the household directory the child
// join page reads: public (no session), non-parent users only, and nothing
// beyond name/display_name/color (no roles, no age groups, no PIN hints).
func TestUsersPublicListsOnlyChildren(t *testing.T) {
	ts, _ := newJoinSecurityServer(t, "")

	resp, err := http.Get(ts.URL + "/api/users/public")
	if err != nil {
		t.Fatalf("GET /api/users/public: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var users []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("listed %d users, want exactly the 2 children", len(users))
	}
	for _, u := range users {
		name, _ := u["name"].(string)
		if name == "parent" {
			t.Errorf("parent listed in public directory: %v", u)
		}
		if _, hasRole := u["role"]; hasRole {
			t.Errorf("role leaked in public directory for %q: %v", name, u)
		}
		if _, hasAge := u["age_group"]; hasAge {
			t.Errorf("age_group leaked in public directory for %q: %v", name, u)
		}
		if _, hasPIN := u["pin"]; hasPIN {
			t.Errorf("pin leaked in public directory for %q: %v", name, u)
		}
	}
}

// sseCaptureWriter is a minimal http.ResponseWriter + http.Flusher that
// records the headers the handler sets. handleStream sets its response
// headers synchronously at entry and only blocks on a 5s ticker for body
// writes, so a capture writer is enough to observe the header contract
// without waiting for the first tick.
type sseCaptureWriter struct {
	header http.Header
}

func (w *sseCaptureWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *sseCaptureWriter) WriteHeader(int) {}

func (w *sseCaptureWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w *sseCaptureWriter) Flush() {}

// TestSSEStreamNoWildcardCORS (design-note T22): the SSE endpoint no longer
// sends Access-Control-Allow-Origin: *. The endpoint is session-gated and
// the SameSite=Strict cookie cannot attach cross-origin, so the wildcard
// header only claimed an allowance the gate already denies.
func TestSSEStreamNoWildcardCORS(t *testing.T) {
	_, deps := newJoinSecurityServer(t, "")

	w := &sseCaptureWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/stream", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		deps.s.handleStream(w, req)
	}()
	// handleStream sets its headers synchronously before entering the block
	// loop; give the goroutine a moment, then cancel so it exits the loop.
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	if got := w.header.Get("Access-Control-Allow-Origin"); got == "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the wildcard header removed", got)
	}
	if ct := w.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}

// TestJoinPageServesStatic checks GET /join redirects to the embedded join
// page and GET /join.html serves it (the child's entry point is reachable
// without any session). A no-redirect client is used so the 303 is observed
// directly rather than followed to the 200 page.
func TestJoinPageServesStatic(t *testing.T) {
	ts, _ := newJoinSecurityServer(t, "")

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := noRedirect.Get(ts.URL + "/join")
	if err != nil {
		t.Fatalf("GET /join: %v", err)
	}
	drain(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("GET /join status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "join.html") {
		t.Errorf("Location = %q, want a redirect to join.html", loc)
	}

	page, err := http.Get(ts.URL + "/join.html")
	if err != nil {
		t.Fatalf("GET /join.html: %v", err)
	}
	defer page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Errorf("GET /join.html status = %d, want 200", page.StatusCode)
	}
}
