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
	"github.com/famclaw/famclaw/internal/familystate"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/store"
)

// newFamilyStateTestServer wires a *Server with familystate.Store backed by
// an in-memory DB, plus a tiny family config used for subject validation.
// Auth middleware is intentionally bypassed — these tests exercise handler
// behaviour. End-to-end auth is covered in middleware tests.
func newFamilyStateTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv := &Server{
		cfg: &config.Config{
			Users: []config.UserConfig{
				{Name: "dep", DisplayName: "Dep", Role: "parent"},
				{Name: "julia", DisplayName: "Julia", Role: "parent"},
				{Name: "teo", DisplayName: "Teo", Role: "child", AgeGroup: "age_13_17"},
			},
		},
		db:          db,
		identStore:  identity.NewStore(db),
		familyState: familystate.NewStore(db),
		cfgMu:       sync.RWMutex{},
	}
	return srv, func() { _ = db.Close() }
}

func TestFamilyStateFacts_Post_Upsert(t *testing.T) {
	cases := []struct {
		name       string
		body       map[string]any
		wantStatus int
		wantSub    string // substring expected in error body (if any)
	}{
		{
			name:       "happy path",
			body:       map[string]any{"category": "pets", "subject": "family", "label": "Stella", "value": "cat"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing required field",
			body:       map[string]any{"category": "pets", "subject": "family", "label": "x"},
			wantStatus: http.StatusBadRequest,
			wantSub:    "required",
		},
		{
			name:       "unknown subject",
			body:       map[string]any{"category": "pets", "subject": "ghost", "label": "x", "value": "y"},
			wantStatus: http.StatusBadRequest,
			wantSub:    "unknown subject",
		},
		{
			name:       "label too long",
			body:       map[string]any{"category": "pets", "subject": "family", "label": longString(65), "value": "y"},
			wantStatus: http.StatusBadRequest,
			wantSub:    "label too long",
		},
		{
			name:       "unknown category",
			body:       map[string]any{"category": "made_up", "subject": "family", "label": "x", "value": "y"},
			wantStatus: http.StatusBadRequest,
			wantSub:    "unknown category",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, cleanup := newFamilyStateTestServer(t)
			defer cleanup()

			body, _ := json.Marshal(tc.body)
			req := httptest.NewRequest(http.MethodPost, "/api/family-state/facts", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			srv.handleFamilyStateFacts(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantSub != "" && !bytes.Contains(rec.Body.Bytes(), []byte(tc.wantSub)) {
				t.Errorf("body %q missing substring %q", rec.Body.String(), tc.wantSub)
			}
		})
	}
}

func TestFamilyStateFacts_Get_List(t *testing.T) {
	srv, cleanup := newFamilyStateTestServer(t)
	defer cleanup()

	// Seed via the same upsert path the handler uses.
	body := mustMarshal(t, map[string]any{"category": "pets", "subject": "family", "label": "Rex", "value": "dog"})
	rec := httptest.NewRecorder()
	srv.handleFamilyStateFacts(rec, httptest.NewRequest(http.MethodPost, "/api/family-state/facts", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.handleFamilyStateFacts(rec, httptest.NewRequest(http.MethodGet, "/api/family-state/facts?category=pets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got []familystate.Fact
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Label != "Rex" {
		t.Errorf("unexpected list: %+v", got)
	}
}

func TestFamilyStateFact_Delete(t *testing.T) {
	srv, cleanup := newFamilyStateTestServer(t)
	defer cleanup()

	body := mustMarshal(t, map[string]any{"category": "pets", "subject": "family", "label": "Rex", "value": "dog"})
	rec := httptest.NewRecorder()
	srv.handleFamilyStateFacts(rec, httptest.NewRequest(http.MethodPost, "/api/family-state/facts", bytes.NewReader(body)))
	var seeded familystate.Fact
	if err := json.NewDecoder(rec.Body).Decode(&seeded); err != nil {
		t.Fatalf("decode seed: %v", err)
	}

	cases := []struct {
		name       string
		path       string
		method     string
		wantStatus int
	}{
		{name: "delete existing", path: "/api/family-state/facts/" + itoa(seeded.ID), method: http.MethodDelete, wantStatus: http.StatusNoContent},
		{name: "delete idempotent", path: "/api/family-state/facts/9999", method: http.MethodDelete, wantStatus: http.StatusNoContent},
		{name: "bad id", path: "/api/family-state/facts/not-a-number", method: http.MethodDelete, wantStatus: http.StatusBadRequest},
		{name: "method not allowed", path: "/api/family-state/facts/1", method: http.MethodGet, wantStatus: http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.handleFamilyStateFact(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.wantStatus {
				t.Errorf("status=%d want=%d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestFamilyStateCategories_Post(t *testing.T) {
	cases := []struct {
		name       string
		body       map[string]any
		wantStatus int
		wantSub    string
	}{
		{name: "happy path", body: map[string]any{"name": "hobbies", "description": "things we do"}, wantStatus: http.StatusOK},
		{name: "missing description", body: map[string]any{"name": "x"}, wantStatus: http.StatusBadRequest, wantSub: "required"},
		{name: "invalid name uppercase", body: map[string]any{"name": "Hobbies", "description": "x"}, wantStatus: http.StatusBadRequest, wantSub: "invalid category name"},
		{name: "invalid name spaces", body: map[string]any{"name": "movie night", "description": "x"}, wantStatus: http.StatusBadRequest, wantSub: "invalid category name"},
		{name: "refuses builtin upsert", body: map[string]any{"name": "allergies", "description": "evil"}, wantStatus: http.StatusBadRequest, wantSub: "built-in"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, cleanup := newFamilyStateTestServer(t)
			defer cleanup()
			body := mustMarshal(t, tc.body)
			req := httptest.NewRequest(http.MethodPost, "/api/family-state/categories", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			srv.handleFamilyStateCategories(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status=%d want=%d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantSub != "" && !bytes.Contains(rec.Body.Bytes(), []byte(tc.wantSub)) {
				t.Errorf("body %q missing %q", rec.Body.String(), tc.wantSub)
			}
		})
	}
}

func TestFamilyStateCategory_Delete(t *testing.T) {
	srv, cleanup := newFamilyStateTestServer(t)
	defer cleanup()

	// Seed a custom category.
	body := mustMarshal(t, map[string]any{"name": "to_delete", "description": "tmp"})
	rec := httptest.NewRecorder()
	srv.handleFamilyStateCategories(rec, httptest.NewRequest(http.MethodPost, "/api/family-state/categories", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed: %s", rec.Body.String())
	}

	cases := []struct {
		name       string
		path       string
		wantStatus int
		wantSub    string
	}{
		{name: "delete custom empty", path: "/api/family-state/categories/to_delete", wantStatus: http.StatusNoContent},
		{name: "delete builtin refused", path: "/api/family-state/categories/allergies", wantStatus: http.StatusBadRequest, wantSub: "built-in"},
		{name: "delete unknown 404", path: "/api/family-state/categories/never_existed", wantStatus: http.StatusNotFound, wantSub: "unknown category"},
		{name: "empty name 400", path: "/api/family-state/categories/", wantStatus: http.StatusBadRequest, wantSub: "bad name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.handleFamilyStateCategory(rec, httptest.NewRequest(http.MethodDelete, tc.path, nil))
			if rec.Code != tc.wantStatus {
				t.Errorf("status=%d want=%d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantSub != "" && !bytes.Contains(rec.Body.Bytes(), []byte(tc.wantSub)) {
				t.Errorf("body %q missing %q", rec.Body.String(), tc.wantSub)
			}
		})
	}
}

// Small helpers — keep imports tight.
func longString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func itoa(n int64) string {
	return strconvFormatInt(n)
}

// strconvFormatInt avoids importing strconv in tests where we only need
// this once. Tiny and obvious.
func strconvFormatInt(n int64) string {
	// Positive ints only — handler validates id > 0 before we ever stringify.
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
