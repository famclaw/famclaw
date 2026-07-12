package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// Session represents an authenticated web session row.
type Session struct {
	ID        string
	UserID    int64
	CreatedAt time.Time
	ExpiresAt time.Time
	LastSeen  time.Time
	IP        string
	UserAgent string
}

// ErrNoSession is returned by SessionStore.Get when the session row is missing
// or has already expired (expires_at <= now). Callers should treat both cases
// as "not authenticated" — the actual reason never reaches the user.
var ErrNoSession = errors.New("session not found or expired")

// defaultSessionTTL is how long a freshly created web session stays valid.
const defaultSessionTTL = 7 * 24 * time.Hour

// SessionStore persists authenticated web sessions in SQLite. The `now`
// function is injected so tests can advance the clock deterministically; in
// production it is time.Now.
type SessionStore struct {
	db  *sql.DB
	ttl time.Duration
	now func() time.Time
}

// NewSessionStore constructs a SessionStore with a 7-day TTL and time.Now
// as the clock source.
func NewSessionStore(db *sql.DB) *SessionStore {
	return &SessionStore{
		db:  db,
		ttl: defaultSessionTTL,
		now: time.Now,
	}
}

// Create generates a cryptographically random 32-byte session ID, stores it
// in web_sessions with an expires_at = now + ttl, and returns the URL-safe
// base64-encoded ID (43 chars, no padding).
func (s *SessionStore) Create(ctx context.Context, userID int64, ip, ua string) (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generating session id: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(buf[:])

	now := s.now()
	expires := now.Add(s.ttl)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO web_sessions(id, user_id, created_at, expires_at, last_seen, ip, user_agent)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		id, userID, now.Unix(), expires.Unix(), now.Unix(), ip, ua)
	if err != nil {
		return "", fmt.Errorf("insert web_session: %w", err)
	}
	return id, nil
}

// Get returns the session for the given ID. Missing rows AND rows whose
// expires_at <= now both return ErrNoSession. Expired rows are NOT deleted
// inline — DeleteExpired sweeps them on a schedule to keep Get cheap and
// side-effect-free.
func (s *SessionStore) Get(ctx context.Context, sessionID string) (*Session, error) {
	var (
		id                             string
		userID                         int64
		createdAt, expiresAt, lastSeen int64
		ip, userAgent                  string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, created_at, expires_at, last_seen, ip, user_agent
		FROM web_sessions WHERE id = ?`, sessionID).Scan(
		&id, &userID, &createdAt, &expiresAt, &lastSeen, &ip, &userAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("select web_session: %w", err)
	}
	if expiresAt <= s.now().Unix() {
		return nil, ErrNoSession
	}
	return &Session{
		ID:        id,
		UserID:    userID,
		CreatedAt: time.Unix(createdAt, 0).UTC(),
		ExpiresAt: time.Unix(expiresAt, 0).UTC(),
		LastSeen:  time.Unix(lastSeen, 0).UTC(),
		IP:        ip,
		UserAgent: userAgent,
	}, nil
}

// Touch refreshes the last_seen timestamp for a session. Best-effort: no
// rowcount check, since this runs from a goroutine after the response has
// already been written and a concurrent Delete is a benign race.
func (s *SessionStore) Touch(ctx context.Context, sessionID string) error {
	now := s.now()
	_, err := s.db.ExecContext(ctx,
		`UPDATE web_sessions SET last_seen = ?, expires_at = ? WHERE id = ?`,
		now.Unix(), now.Add(s.ttl).Unix(), sessionID)
	if err != nil {
		return fmt.Errorf("touch web_session: %w", err)
	}
	return nil
}

// Delete removes a session. No rowcount check — logging out an already-expired
// (or already-swept) session is not an error from the caller's perspective.
func (s *SessionStore) Delete(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM web_sessions WHERE id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("delete web_session: %w", err)
	}
	return nil
}

// DeleteExpired removes all sessions whose expires_at is in the past, returning
// the number of rows deleted.
func (s *SessionStore) DeleteExpired(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM web_sessions WHERE expires_at <= ?`, s.now().Unix())
	if err != nil {
		return 0, fmt.Errorf("delete expired web_sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return int(n), nil
}
