package middleware_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/web/middleware"

	_ "modernc.org/sqlite"
)

// newTestStore opens a fresh on-disk SQLite database in a t.TempDir and
// returns both the SessionStore and the path to the DB file. The path is
// used by tests that need to bypass the store's API to mutate rows
// directly (e.g. to fake an expired session without poking the store's
// unexported `now` field across package boundaries).
func newTestStore(t *testing.T) (*store.SessionStore, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// SessionStore needs a *sql.DB; we cannot reach store.DB.sql from another
	// package, so open a second handle to the same file. modernc.org/sqlite
	// happily multiplexes connections to the same file (especially under WAL).
	raw, err := sql.Open("sqlite", dbPath+"?_journal=WAL&_timeout=5000&_fk=true")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return store.NewSessionStore(raw), dbPath
}

// rawDB opens a third independent handle on the same path. Tests use this
// to UPDATE web_sessions directly when they need to simulate an expired
// session without manipulating the SessionStore's unexported clock.
func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_journal=WAL&_timeout=5000&_fk=true")
	if err != nil {
		t.Fatalf("rawDB sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// okHandler is the inner handler used by tests that expect the middleware
// to pass through. It records that it ran and stashes the request context
// so tests can call IdentityFrom on it.
type okHandler struct {
	called  bool
	gotCtx  context.Context
	gotID   *middleware.Identity
	gotIDOK bool
}

func (h *okHandler) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	h.called = true
	h.gotCtx = r.Context()
	h.gotID, h.gotIDOK = middleware.IdentityFrom(r.Context())
}

const wantUnauthBody = `{"error":"unauthenticated"}` + "\n"

func TestWithSession_NoCookie(t *testing.T) {
	sessions, _ := newTestStore(t)

	inner := &okHandler{}
	mw := middleware.WithSession(sessions)(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if got := rr.Body.String(); got != wantUnauthBody {
		t.Errorf("body = %q, want %q", got, wantUnauthBody)
	}
	if inner.called {
		t.Errorf("inner handler should not have been called")
	}
}

func TestWithSession_InvalidCookie(t *testing.T) {
	sessions, _ := newTestStore(t)

	inner := &okHandler{}
	mw := middleware.WithSession(sessions)(inner)

	// 43-char base64url string that was never inserted.
	bogus := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "famclaw_session", Value: bogus})
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if got := rr.Body.String(); got != wantUnauthBody {
		t.Errorf("body = %q, want %q", got, wantUnauthBody)
	}
	if inner.called {
		t.Errorf("inner handler should not have been called")
	}
}

func TestWithSession_ExpiredCookie(t *testing.T) {
	sessions, dbPath := newTestStore(t)

	ctx := context.Background()
	id, err := sessions.Create(ctx, 42, "10.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Force the row to be expired by stomping expires_at to 1 (epoch+1s).
	// We cannot reach the SessionStore's unexported `now` field from this
	// external test package, so we drive the same effect through SQL.
	raw := rawDB(t, dbPath)
	if _, err := raw.ExecContext(ctx,
		`UPDATE web_sessions SET expires_at = 1 WHERE id = ?`, id); err != nil {
		t.Fatalf("UPDATE expires_at: %v", err)
	}

	inner := &okHandler{}
	mw := middleware.WithSession(sessions)(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "famclaw_session", Value: id})
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if got := rr.Body.String(); got != wantUnauthBody {
		t.Errorf("body = %q, want %q", got, wantUnauthBody)
	}
	if inner.called {
		t.Errorf("inner handler should not have been called for expired session")
	}
}

func TestWithSession_ValidCookie(t *testing.T) {
	sessions, _ := newTestStore(t)

	ctx := context.Background()
	const wantUserID int64 = 7
	id, err := sessions.Create(ctx, wantUserID, "1.2.3.4", "ua")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	inner := &okHandler{}
	mw := middleware.WithSession(sessions)(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "famclaw_session", Value: id})
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (body=%q)", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !inner.called {
		t.Fatalf("inner handler was not called")
	}
	if !inner.gotIDOK {
		t.Fatalf("IdentityFrom returned ok=false; expected an Identity in ctx")
	}
	if inner.gotID == nil {
		t.Fatalf("IdentityFrom returned nil Identity")
	}
	if inner.gotID.UserID != wantUserID {
		t.Errorf("Identity.UserID = %d, want %d", inner.gotID.UserID, wantUserID)
	}
	if inner.gotID.SessionID != id {
		t.Errorf("Identity.SessionID = %q, want %q", inner.gotID.SessionID, id)
	}
}

// TestWithSession_TouchUpdatesLastSeen verifies that after a request flows
// through the middleware, last_seen is eventually refreshed by the
// background Touch goroutine. We don't have a sync barrier on the goroutine,
// so we poll Get until LastSeen advances past the original value.
func TestWithSession_TouchUpdatesLastSeen(t *testing.T) {
	sessions, _ := newTestStore(t)

	ctx := context.Background()
	id, err := sessions.Create(ctx, 1, "", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Capture the original LastSeen so we can detect any forward movement.
	before, err := sessions.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get before: %v", err)
	}
	originalLastSeen := before.LastSeen

	// Sleep enough that a successful Touch will write a strictly-greater
	// Unix-second value. Without this, Create and Touch could share the
	// same Unix second and the test would be a no-op.
	time.Sleep(1100 * time.Millisecond)

	inner := &okHandler{}
	mw := middleware.WithSession(sessions)(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "famclaw_session", Value: id})
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rr.Code, rr.Body.String())
	}

	// Poll for up to 5s — Touch is fired in a goroutine and may not yet have
	// committed by the time ServeHTTP returns.
	deadline := time.Now().Add(5 * time.Second)
	var after *store.Session
	for time.Now().Before(deadline) {
		after, err = sessions.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get after: %v", err)
		}
		if after.LastSeen.After(originalLastSeen) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if after == nil || !after.LastSeen.After(originalLastSeen) {
		t.Errorf("LastSeen did not advance: before=%v after=%v",
			originalLastSeen, lastSeenStr(after))
	}
}

func lastSeenStr(s *store.Session) string {
	if s == nil {
		return "<nil>"
	}
	return s.LastSeen.String()
}
