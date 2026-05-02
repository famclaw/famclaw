package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/store"
)

const testParentPIN = "1234"

// newTestServer returns a *Server with an in-memory store, identity store,
// one parent (PIN=1234), and one child user "julia". Cleanup closes the db.
func newTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	identStore := identity.NewStore(db)
	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "sarah", DisplayName: "Sarah", Role: "parent", PIN: testParentPIN},
			{Name: "julia", DisplayName: "Julia", Role: "child", AgeGroup: "age_8_12"},
		},
	}
	srv := &Server{
		cfg:        cfg,
		db:         db,
		identStore: identStore,
		cfgMu:      sync.RWMutex{},
	}
	return srv, func() { _ = db.Close() }
}

func TestUnknownAccounts_GET(t *testing.T) {
	cases := []struct {
		name       string
		seed       bool
		setHeader  bool
		pinHeader  string
		method     string
		wantStatus int
		wantLen    int
	}{
		{"no PIN", false, false, "", http.MethodGet, http.StatusForbidden, 0},
		{"wrong PIN", false, true, "9999", http.MethodGet, http.StatusForbidden, 0},
		{"correct PIN empty", false, true, testParentPIN, http.MethodGet, http.StatusOK, 0},
		{"correct PIN with row", true, true, testParentPIN, http.MethodGet, http.StatusOK, 1},
		{"method not allowed", false, true, testParentPIN, http.MethodPost, http.StatusMethodNotAllowed, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, cleanup := newTestServer(t)
			defer cleanup()

			if tc.seed {
				if err := srv.identStore.RecordUnknown("telegram", "X1", "Julia"); err != nil {
					t.Fatalf("RecordUnknown: %v", err)
				}
			}

			req := httptest.NewRequest(tc.method, "/api/unknown-accounts", nil)
			if tc.setHeader {
				req.Header.Set("X-Parent-PIN", tc.pinHeader)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				var got []store.UnknownAccount
				if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
					t.Fatalf("decode body: %v (raw: %s)", err, rec.Body.String())
				}
				if len(got) != tc.wantLen {
					t.Errorf("len = %d, want %d", len(got), tc.wantLen)
				}
			}
		})
	}
}

func TestUnknownAccounts_LinkAndDismiss(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()

	for _, ext := range []string{"X1", "X2"} {
		if err := srv.identStore.RecordUnknown("telegram", ext, "Julia"); err != nil {
			t.Fatalf("RecordUnknown %s: %v", ext, err)
		}
	}

	// Link X1 → julia
	linkBody := []byte(`{"gateway":"telegram","external_id":"X1","user_name":"julia"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/unknown-accounts/link", bytes.NewReader(linkBody))
	req.Header.Set("X-Parent-PIN", testParentPIN)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("link status = %d, body=%s", rec.Code, rec.Body.String())
	}

	// Dismiss X2
	dismissBody := []byte(`{"gateway":"telegram","external_id":"X2"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/unknown-accounts/dismiss", bytes.NewReader(dismissBody))
	req.Header.Set("X-Parent-PIN", testParentPIN)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dismiss status = %d, body=%s", rec.Code, rec.Body.String())
	}

	list, err := srv.identStore.ListUnknown()
	if err != nil {
		t.Fatalf("ListUnknown: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("ListUnknown len = %d, want 0", len(list))
	}

	user, err := srv.identStore.Resolve("telegram", "X1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if user == nil || user.Name != "julia" {
		t.Errorf("Resolve = %+v, want user julia", user)
	}
}

func TestUnknownAccounts_LinkUnknownUser(t *testing.T) {
	srv, cleanup := newTestServer(t)
	defer cleanup()

	if err := srv.identStore.RecordUnknown("telegram", "X1", "Julia"); err != nil {
		t.Fatalf("RecordUnknown: %v", err)
	}

	body := []byte(`{"gateway":"telegram","external_id":"X1","user_name":"ghost"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/unknown-accounts/link", bytes.NewReader(body))
	req.Header.Set("X-Parent-PIN", testParentPIN)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}

	list, err := srv.identStore.ListUnknown()
	if err != nil {
		t.Fatalf("ListUnknown: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("row should remain; got len=%d", len(list))
	}
}
