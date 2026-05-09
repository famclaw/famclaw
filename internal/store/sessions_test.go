package store

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// newTestSessionStore opens a real on-disk SQLite DB (matching the project's
// test pattern — no in-memory shortcut), then wraps the underlying *sql.DB
// in a SessionStore. The returned cleanup closes the DB.
func newTestSessionStore(t *testing.T) (*SessionStore, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return NewSessionStore(db.sql), func() { _ = db.Close() }
}

// fixedClock returns a function suitable for SessionStore.now whose returned
// time is whatever t points at when called. Tests mutate *t to advance the clock.
func fixedClock(t *time.Time) func() time.Time {
	return func() time.Time { return *t }
}

func TestSessionStore(t *testing.T) {
	cases := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{
			name: "CreateAndGet",
			fn: func(t *testing.T) {
				s, cleanup := newTestSessionStore(t)
				defer cleanup()

				now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
				s.now = func() time.Time { return now }

				ctx := context.Background()
				id, err := s.Create(ctx, 42, "10.0.0.1", "Mozilla/5.0")
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if id == "" {
					t.Fatal("Create returned empty id")
				}

				sess, err := s.Get(ctx, id)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if sess.ID != id {
					t.Errorf("ID = %q, want %q", sess.ID, id)
				}
				if sess.UserID != 42 {
					t.Errorf("UserID = %d, want 42", sess.UserID)
				}
				if sess.IP != "10.0.0.1" {
					t.Errorf("IP = %q, want %q", sess.IP, "10.0.0.1")
				}
				if sess.UserAgent != "Mozilla/5.0" {
					t.Errorf("UserAgent = %q, want %q", sess.UserAgent, "Mozilla/5.0")
				}

				// CreatedAt should match `now` to within 1s (Unix-second resolution).
				if delta := sess.CreatedAt.Sub(now); delta < -time.Second || delta > time.Second {
					t.Errorf("CreatedAt = %v, want within 1s of %v (delta=%v)", sess.CreatedAt, now, delta)
				}
				// ExpiresAt should be CreatedAt + 7 days, within 1s tolerance.
				wantExpires := sess.CreatedAt.Add(7 * 24 * time.Hour)
				if delta := sess.ExpiresAt.Sub(wantExpires); delta < -time.Second || delta > time.Second {
					t.Errorf("ExpiresAt = %v, want within 1s of CreatedAt+7d (%v), delta=%v",
						sess.ExpiresAt, wantExpires, delta)
				}
				// LastSeen should also match `now` initially.
				if delta := sess.LastSeen.Sub(now); delta < -time.Second || delta > time.Second {
					t.Errorf("LastSeen = %v, want within 1s of %v", sess.LastSeen, now)
				}
			},
		},
		{
			name: "GetMissingReturnsErrNoSession",
			fn: func(t *testing.T) {
				s, cleanup := newTestSessionStore(t)
				defer cleanup()

				ctx := context.Background()
				// 43-char base64url id that we never inserted.
				bogus := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
				got, err := s.Get(ctx, bogus)
				if got != nil {
					t.Errorf("Get returned non-nil session %v for missing id", got)
				}
				if !errors.Is(err, ErrNoSession) {
					t.Errorf("err = %v, want errors.Is(err, ErrNoSession)", err)
				}
			},
		},
		{
			name: "GetExpiredReturnsErrNoSession",
			fn: func(t *testing.T) {
				s, cleanup := newTestSessionStore(t)
				defer cleanup()

				now := time.Date(2026, 2, 1, 9, 30, 0, 0, time.UTC)
				clock := now
				s.now = fixedClock(&clock)

				ctx := context.Background()
				id, err := s.Create(ctx, 7, "1.2.3.4", "ua")
				if err != nil {
					t.Fatalf("Create: %v", err)
				}

				// Advance the clock 8 days into the future — well past the 7-day TTL.
				clock = now.Add(8 * 24 * time.Hour)

				got, err := s.Get(ctx, id)
				if got != nil {
					t.Errorf("Get returned non-nil session %v for expired id", got)
				}
				if !errors.Is(err, ErrNoSession) {
					t.Errorf("err = %v, want errors.Is(err, ErrNoSession)", err)
				}
			},
		},
		{
			name: "Touch",
			fn: func(t *testing.T) {
				s, cleanup := newTestSessionStore(t)
				defer cleanup()

				t0 := time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC)
				clock := t0
				s.now = fixedClock(&clock)

				ctx := context.Background()
				id, err := s.Create(ctx, 1, "", "")
				if err != nil {
					t.Fatalf("Create: %v", err)
				}

				// Advance 1 hour, then Touch.
				clock = t0.Add(1 * time.Hour)
				if err := s.Touch(ctx, id); err != nil {
					t.Fatalf("Touch: %v", err)
				}

				sess, err := s.Get(ctx, id)
				if err != nil {
					t.Fatalf("Get after Touch: %v", err)
				}
				want := t0.Add(1 * time.Hour)
				if delta := sess.LastSeen.Sub(want); delta < -time.Second || delta > time.Second {
					t.Errorf("LastSeen = %v, want within 1s of %v (delta=%v)", sess.LastSeen, want, delta)
				}
			},
		},
		{
			name: "Delete",
			fn: func(t *testing.T) {
				s, cleanup := newTestSessionStore(t)
				defer cleanup()

				ctx := context.Background()
				id, err := s.Create(ctx, 5, "", "")
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if err := s.Delete(ctx, id); err != nil {
					t.Fatalf("Delete: %v", err)
				}
				got, err := s.Get(ctx, id)
				if got != nil {
					t.Errorf("Get returned non-nil session %v after Delete", got)
				}
				if !errors.Is(err, ErrNoSession) {
					t.Errorf("err = %v, want errors.Is(err, ErrNoSession)", err)
				}
			},
		},
		{
			name: "DeleteExpired",
			fn: func(t *testing.T) {
				s, cleanup := newTestSessionStore(t)
				defer cleanup()

				t0 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
				clock := t0
				s.now = fixedClock(&clock)

				ctx := context.Background()
				var oldIDs []string
				for i := 0; i < 3; i++ {
					id, err := s.Create(ctx, int64(100+i), "", "")
					if err != nil {
						t.Fatalf("Create old #%d: %v", i, err)
					}
					oldIDs = append(oldIDs, id)
				}

				// Advance past TTL, then create one fresh session.
				clock = t0.Add(8 * 24 * time.Hour)
				freshID, err := s.Create(ctx, 999, "", "")
				if err != nil {
					t.Fatalf("Create fresh: %v", err)
				}

				n, err := s.DeleteExpired(ctx)
				if err != nil {
					t.Fatalf("DeleteExpired: %v", err)
				}
				if n != 3 {
					t.Errorf("DeleteExpired returned %d, want 3", n)
				}

				for i, id := range oldIDs {
					got, err := s.Get(ctx, id)
					if got != nil {
						t.Errorf("old[%d] Get returned non-nil after sweep", i)
					}
					if !errors.Is(err, ErrNoSession) {
						t.Errorf("old[%d] err = %v, want ErrNoSession", i, err)
					}
				}

				sess, err := s.Get(ctx, freshID)
				if err != nil {
					t.Fatalf("Get fresh after sweep: %v", err)
				}
				if sess == nil || sess.ID != freshID {
					t.Errorf("fresh session lost: got %v", sess)
				}
			},
		},
		{
			name: "IDLengthAndAlphabet",
			fn: func(t *testing.T) {
				s, cleanup := newTestSessionStore(t)
				defer cleanup()

				alphabet := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
				ctx := context.Background()
				seen := make(map[string]struct{}, 100)
				for i := 0; i < 100; i++ {
					id, err := s.Create(ctx, int64(i), "", "")
					if err != nil {
						t.Fatalf("Create #%d: %v", i, err)
					}
					if len(id) != 43 {
						t.Errorf("id #%d length = %d, want 43 (id=%q)", i, len(id), id)
					}
					if !alphabet.MatchString(id) {
						t.Errorf("id #%d does not match base64url alphabet: %q", i, id)
					}
					if _, dup := seen[id]; dup {
						t.Errorf("id #%d is a duplicate: %q", i, id)
					}
					seen[id] = struct{}{}
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, tc.fn)
	}
}
