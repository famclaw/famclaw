package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/famclaw/famclaw/internal/familystate"
)

// TestFamilyStateAuth is the F8 regression test: family-state reads (GET) stay
// open to every household member, while mutations (POST/DELETE) are
// manager-only. It drives the live mux — real session middleware, real store
// sessions, and the handler-level requireManager gate — so a regression that
// wraps these routes in protectParent (403 for child GETs) fails here, and so
// a child 403 is pinned to leave state untouched.
func TestFamilyStateAuth(t *testing.T) {
	ts, s, sessions, db := newPrincipalTestServer(t, filepath.Join(t.TempDir(), "test.db"), principalTestUsers())
	ctx := context.Background()

	// The fixture does not wire family state; handlers read s.familyState per
	// request, so attach it before any traffic.
	s.familyState = familystate.NewStore(db)

	parentSess, err := sessions.Create(ctx, "parent", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create (parent): %v", err)
	}
	childSess, err := sessions.Create(ctx, "emma", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create (emma): %v", err)
	}

	// Seed (as the parent): two categories, one of them holding a fact.
	seed := func(path string, body []byte) {
		t.Helper()
		if code, b := doAuthReq(t, ts, http.MethodPost, path, parentSess, body); code != http.StatusOK {
			t.Fatalf("seed %s: code=%d body=%s", path, code, b)
		}
	}
	seed("/api/family-state/categories", mustMarshal(t, map[string]any{"name": "hobbies", "description": "things we do"}))
	seed("/api/family-state/categories", mustMarshal(t, map[string]any{"name": "watchlist", "description": "shows to watch"}))

	code, body := doAuthReq(t, ts, http.MethodPost, "/api/family-state/facts", parentSess,
		mustMarshal(t, map[string]any{"category": "hobbies", "subject": "family", "label": "board games", "value": "weekly"}))
	if code != http.StatusOK {
		t.Fatalf("seed fact: code=%d body=%s", code, body)
	}
	var seeded struct {
		ID int64 `json:"ID"`
	}
	if err := json.Unmarshal([]byte(body), &seeded); err != nil {
		t.Fatalf("decode seeded fact: %v", err)
	}

	// Child-visible snapshots — reads are child-open, so these also assert
	// the GET path works for a child at every step.
	factsAs := func(t *testing.T, sid string) []map[string]any {
		code, b := doAuthReq(t, ts, http.MethodGet, "/api/family-state/facts", sid, nil)
		if code != http.StatusOK {
			t.Fatalf("GET facts: code=%d body=%s", code, b)
		}
		var out []map[string]any
		if err := json.Unmarshal([]byte(b), &out); err != nil {
			t.Fatalf("decode facts: %v", err)
		}
		return out
	}
	catsAs := func(t *testing.T, sid string) []string {
		code, b := doAuthReq(t, ts, http.MethodGet, "/api/family-state/categories", sid, nil)
		if code != http.StatusOK {
			t.Fatalf("GET categories: code=%d body=%s", code, b)
		}
		var out []map[string]any
		if err := json.Unmarshal([]byte(b), &out); err != nil {
			t.Fatalf("decode categories: %v", err)
		}
		names := make([]string, 0, len(out))
		for _, c := range out {
			names = append(names, c["Name"].(string))
		}
		return names
	}
	hasFactLabel := func(t *testing.T, sid, label string) bool {
		for _, f := range factsAs(t, sid) {
			if f["Label"] == label {
				return true
			}
		}
		return false
	}
	hasFactID := func(t *testing.T, sid string, id int64) bool {
		for _, f := range factsAs(t, sid) {
			if f["ID"].(float64) == float64(id) {
				return true
			}
		}
		return false
	}
	hasCat := func(t *testing.T, sid, name string) bool {
		for _, n := range catsAs(t, sid) {
			if n == name {
				return true
			}
		}
		return false
	}

	factBody := mustMarshal(t, map[string]any{"category": "hobbies", "subject": "family", "label": "movie night", "value": "friday"})
	catBody := mustMarshal(t, map[string]any{"name": "blocked_cat", "description": "must not exist"})

	cases := []struct {
		name       string
		method     string
		path       string
		body       []byte
		wantNoSess int
		wantChild  int
		wantParent int
		// childNoChange pins "zero state changes on a child 403".
		childNoChange func(t *testing.T)
		// parentChanged pins that the parent's success actually mutated.
		parentChanged func(t *testing.T)
	}{
		{
			name:       "GET facts",
			method:     http.MethodGet,
			path:       "/api/family-state/facts",
			wantNoSess: http.StatusUnauthorized,
			wantChild:  http.StatusOK,
			wantParent: http.StatusOK,
		},
		{
			name:       "POST facts",
			method:     http.MethodPost,
			path:       "/api/family-state/facts",
			body:       factBody,
			wantNoSess: http.StatusUnauthorized,
			wantChild:  http.StatusForbidden,
			wantParent: http.StatusOK,
			childNoChange: func(t *testing.T) {
				if hasFactLabel(t, childSess, "movie night") {
					t.Errorf("child 403 still created the fact")
				}
			},
			parentChanged: func(t *testing.T) {
				if !hasFactLabel(t, parentSess, "movie night") {
					t.Errorf("parent POST did not create the fact")
				}
			},
		},
		{
			name:       "DELETE fact",
			method:     http.MethodDelete,
			path:       "/api/family-state/facts/" + itoa(seeded.ID),
			wantNoSess: http.StatusUnauthorized,
			wantChild:  http.StatusForbidden,
			wantParent: http.StatusNoContent,
			childNoChange: func(t *testing.T) {
				if !hasFactID(t, childSess, seeded.ID) {
					t.Errorf("child 403 deleted the seeded fact")
				}
			},
			parentChanged: func(t *testing.T) {
				if hasFactID(t, parentSess, seeded.ID) {
					t.Errorf("parent DELETE did not remove the seeded fact")
				}
			},
		},
		{
			name:       "GET categories",
			method:     http.MethodGet,
			path:       "/api/family-state/categories",
			wantNoSess: http.StatusUnauthorized,
			wantChild:  http.StatusOK,
			wantParent: http.StatusOK,
		},
		{
			name:       "POST categories",
			method:     http.MethodPost,
			path:       "/api/family-state/categories",
			body:       catBody,
			wantNoSess: http.StatusUnauthorized,
			wantChild:  http.StatusForbidden,
			wantParent: http.StatusOK,
			childNoChange: func(t *testing.T) {
				if hasCat(t, childSess, "blocked_cat") {
					t.Errorf("child 403 still created the category")
				}
			},
			parentChanged: func(t *testing.T) {
				if !hasCat(t, parentSess, "blocked_cat") {
					t.Errorf("parent POST did not create the category")
				}
			},
		},
		{
			name:       "DELETE category",
			method:     http.MethodDelete,
			path:       "/api/family-state/categories/watchlist",
			wantNoSess: http.StatusUnauthorized,
			wantChild:  http.StatusForbidden,
			wantParent: http.StatusNoContent,
			childNoChange: func(t *testing.T) {
				if !hasCat(t, childSess, "watchlist") {
					t.Errorf("child 403 deleted the seeded category")
				}
			},
			parentChanged: func(t *testing.T) {
				if hasCat(t, parentSess, "watchlist") {
					t.Errorf("parent DELETE did not remove the category")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// No session cookie at all -> 401 from the session middleware.
			if code, b := doAuthReq(t, ts, tc.method, tc.path, "", tc.body); code != tc.wantNoSess {
				t.Fatalf("no-session %s %s = %d, want %d (body=%s)", tc.method, tc.path, code, tc.wantNoSess, b)
			}
			// Child session -> 403 on mutations, 200 on reads.
			if code, b := doAuthReq(t, ts, tc.method, tc.path, childSess, tc.body); code != tc.wantChild {
				t.Fatalf("child %s %s = %d, want %d (body=%s)", tc.method, tc.path, code, tc.wantChild, b)
			}
			if tc.childNoChange != nil {
				tc.childNoChange(t)
			}
			// Parent session -> the mutation lands (or the read succeeds).
			if code, b := doAuthReq(t, ts, tc.method, tc.path, parentSess, tc.body); code != tc.wantParent {
				t.Fatalf("parent %s %s = %d, want %d (body=%s)", tc.method, tc.path, code, tc.wantParent, b)
			}
			if tc.parentChanged != nil {
				tc.parentChanged(t)
			}
		})
	}
}

// doAuthReq issues a request against the live server, carrying the session
// cookie when sid is non-empty ("" = anonymous). Returns status + body.
func doAuthReq(t *testing.T, ts *httptest.Server, method, path, sid string, payload []byte) (int, string) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, ts.URL+path, body)
	if err != nil {
		t.Fatalf("NewRequest %s %s: %v", method, path, err)
	}
	if sid != "" {
		req.AddCookie(&http.Cookie{Name: "famclaw_session", Value: sid})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
