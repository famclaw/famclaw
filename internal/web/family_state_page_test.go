package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/familystate"
)

// readFamilyStatePage returns the embedded family-state page source.
func readFamilyStatePage(t *testing.T) string {
	t.Helper()
	data, err := staticFiles.ReadFile("static/family-state.html")
	if err != nil {
		t.Fatalf("read embedded family-state.html: %v", err)
	}
	return string(data)
}

// TestFamilyStatePageServed guards the route itself: the refreshed page must
// be reachable through the real Handler() at /family-state.html and come back
// 200 as text/html. A broken embed path or a renamed static file would show
// up here as a 404 from the static file server.
func TestFamilyStatePageServed(t *testing.T) {
	srv, cleanup := newFamilyStateTestServer(t)
	defer cleanup()

	h := srv.Handler()
	req := httptest.NewRequest(http.MethodGet, "/family-state.html", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html*", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<a class="nav-btn" href="/">← Dashboard</a>`) {
		t.Errorf("served page missing dashboard back-link")
	}
}

// TestFamilyStatePageDesignContract pins the refreshed design contract added
// in PR #410: the shared design-token block, the two card sections (Categories
// and Facts) with their .sec-head/.table-scroll structure, and the header
// back-link. These are the markers a reviewer approved; a later edit that
// drops them changes the page under review's contract.
func TestFamilyStatePageDesignContract(t *testing.T) {
	src := readFamilyStatePage(t)

	cases := []struct{ name, needle string }{
		{"design token block", ":root{"},
		{"primary token", "--c-primary:#6366f1"},
		{"radius token", "--radius:12px"},
		{"theme-color meta", `<meta name="theme-color" content="#6366f1">`},
		{"header nav button style", "header .nav-btn{"},
		{"categories section id", `<section id="categories">`},
		{"facts section id", `<section id="facts">`},
		{"categories sec-head", `<div class="sec-head"><span class="sec-title">Categories</span></div>`},
		{"facts sec-head", `<div class="sec-head"><span class="sec-title">Facts</span></div>`},
		{"back-link", `<a class="nav-btn" href="/">← Dashboard</a>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(src, tc.needle) {
				t.Errorf("family-state.html missing %q", tc.needle)
			}
		})
	}

	// Each card wraps its table in exactly one .sec-head and one
	// .table-scroll — count so a section losing its wrapper is caught.
	for _, cls := range []string{`class="sec-head"`, `class="table-scroll"`} {
		if got := strings.Count(src, cls); got != 2 {
			t.Errorf("count of %s = %d, want 2 (one per section)", cls, got)
		}
	}
}

// TestFamilyStatePageNoLegacyDesign is the regression guard against the
// pre-refresh page coming back. The markers below are distinctive strings from
// the old design (git show origin/main:internal/web/static/family-state.html):
// the <nav> back-link wrapper, the <h2> section headings, and the old inline
// palette values. None of them belong in the token-based refresh.
func TestFamilyStatePageNoLegacyDesign(t *testing.T) {
	src := readFamilyStatePage(t)

	cases := []struct{ name, needle string }{
		{"old nav back-link", `<nav><a href="/">`},
		{"old categories heading", `<h2>Categories</h2>`},
		{"old facts heading", `<h2>Facts</h2>`},
		{"old table header palette", `background: #fafafa`},
		{"old link colour", `color: #2a6`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(src, tc.needle) {
				t.Errorf("family-state.html resurrected legacy marker %q", tc.needle)
			}
		})
	}
}

// TestFamilyStatePageFetchPathsAreRouted reads every /api/... path the page's
// JS fetches and checks two things: each one is a known family-state route (no
// dead endpoint like the old /api/setup/ollama trap), and each is actually
// mounted on Handler() — an unregistered /api/... path would fall through to
// the static handler and 404, while a mounted one fails closed with 401 when
// no session exists (sessions is nil in this fixture).
func TestFamilyStatePageFetchPathsAreRouted(t *testing.T) {
	src := readFamilyStatePage(t)

	// Every quoted /api/ literal in the page (fetch is called via api(path)).
	re := regexp.MustCompile(`'(/api/[^']*)'`)
	matches := re.FindAllStringSubmatch(src, -1)
	if len(matches) == 0 {
		t.Fatal("no /api/ fetch paths found in family-state.html — did the page stop calling the API?")
	}
	known := map[string]bool{
		"/api/family-state/categories": true,
		"/api/family-state/facts":      true,
	}
	seen := map[string]bool{}
	for _, m := range matches {
		p := strings.TrimSuffix(m[1], "/") // trailing slash marks the /{id} form
		if !known[p] {
			t.Errorf("page fetches %q — not a known family-state API route", m[1])
			continue
		}
		seen[p] = true
	}
	for _, p := range []string{"/api/family-state/categories", "/api/family-state/facts"} {
		if !seen[p] {
			t.Errorf("page no longer fetches %s", p)
		}
	}

	srv, cleanup := newFamilyStateTestServer(t)
	defer cleanup()
	h := srv.Handler()
	for _, p := range []string{"/api/family-state/categories", "/api/family-state/facts"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		// Route mounted behind s.protect with no session store: fail closed 401.
		// An unmounted route would fall through to the static handler with 404.
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s through Handler() = %d, want 401 (route must be mounted and auth-gated)", p, rec.Code)
		}
	}
}

// TestFamilyStateCategories_Get_List covers GET /api/family-state/categories —
// the page's refreshCategories() reads it on load, and handler-level coverage
// of the list shape was missing. The assertions pin exactly the JSON keys the
// page JS reads (Name, Description, AlwaysInject, IsBuiltin) so a rename on
// the Go side breaks here instead of silently blanking the table in the UI.
func TestFamilyStateCategories_Get_List(t *testing.T) {
	srv, cleanup := newFamilyStateTestServer(t)
	defer cleanup()

	if err := srv.familyState.UpsertCategory(context.Background(), &familystate.Category{
		Name: "hobbies", Description: "things we do", AlwaysInject: true,
	}); err != nil {
		t.Fatalf("seed category: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.handleFamilyStateCategories(rec, httptest.NewRequest(http.MethodGet, "/api/family-state/categories", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Decode through a generic map so the wire keys — not just Go field
	// names — are what the page JS will see.
	var raw []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byName := map[string]map[string]any{}
	for _, c := range raw {
		name, _ := c["Name"].(string)
		byName[name] = c
	}

	// Built-in seed must be listed and flagged (page renders 'built-in' chip
	// instead of a delete button based on this flag).
	if all := byName["allergies"]; all == nil {
		t.Fatalf("built-in category 'allergies' missing from list: %s", rec.Body.String())
	} else if isB, _ := all["IsBuiltin"].(bool); !isB {
		t.Errorf("allergies IsBuiltin = %v, want true", all["IsBuiltin"])
	}
	if h := byName["hobbies"]; h == nil {
		t.Fatalf("custom category 'hobbies' missing from list: %s", rec.Body.String())
	} else {
		if desc, _ := h["Description"].(string); desc != "things we do" {
			t.Errorf("hobbies Description = %q, want %q", desc, "things we do")
		}
		if ai, _ := h["AlwaysInject"].(bool); !ai {
			t.Errorf("hobbies AlwaysInject = %v, want true", h["AlwaysInject"])
		}
		if isB, _ := h["IsBuiltin"].(bool); isB {
			t.Errorf("hobbies IsBuiltin = %v, want false", h["IsBuiltin"])
		}
	}

	// The page reads exactly these four keys off every row; anything else on
	// the row (timestamps) is tolerated, but a rename breaks the table.
	for _, key := range []string{"Name", "Description", "AlwaysInject", "IsBuiltin"} {
		if _, ok := byName["hobbies"][key]; !ok {
			t.Errorf("category row missing wire key %q read by page JS: %v", key, byName["hobbies"])
		}
	}
}
