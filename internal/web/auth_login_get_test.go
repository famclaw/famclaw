package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleLogin_GET_RedirectsToLoginHTML is the regression guard for the
// v0.5.1/v0.5.2 bug where browser GET to /login returned HTTP 405. The /login
// route is mounted on the POST-only AuthHandler.HandleLogin; handleRoot has
// a /login -> /login.html redirect that was dead code because Go's
// http.ServeMux picks the more specific pattern. Fixed in PR #136 by having
// HandleLogin 303-redirect GET to /login.html.
//
// This test lives in its own file (not auth_test.go) so future edits do not
// have to rewrite the 17KB auth_test.go via a single tool-call payload.
func TestHandleLogin_GET_RedirectsToLoginHTML(t *testing.T) {
	deps, cleanup := newAuthTestDeps(t)
	defer cleanup()
	h := deps.handler()

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	h.HandleLogin(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET /login status = %d, want %d (See Other)", rec.Code, http.StatusSeeOther)
	}
	if loc := rec.Header().Get("Location"); loc != "/login.html" {
		t.Errorf("Location = %q, want /login.html", loc)
	}
}
