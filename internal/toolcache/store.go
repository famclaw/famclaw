package toolcache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound is returned when a lookup misses or is denied (cross-user
// access returns ErrNotFound rather than ErrForbidden so the existence
// of cross-user rows is not leaked).
var ErrNotFound = errors.New("toolcache: not found")

// cacheRow mirrors the tool_result_cache table.
type cacheRow struct {
	ID          string
	UserName    string
	ConvID      string
	ToolName    string
	ArgsHash    string
	PayloadPath string
	Bytes       int64
	ContentType string
	CreatedAt   int64
	ExpiresAt   int64
	AccessedAt  int64
}

// auditRow mirrors the tool_result_audit table.
type auditRow struct {
	ID              string
	UserName        string
	ConvID          string
	ToolName        string
	ArgsHash        string
	ArgsSummary     string
	Bytes           int64
	ContentType     string
	Category        sql.NullString
	CreatedAt       int64
	PayloadID       sql.NullString
	PayloadPurgedAt sql.NullInt64
}

// store wraps the *sql.DB with the queries this package needs. Kept as an
// internal type so tests can swap to an in-memory sqlite without exposing
// the DB to Cache callers. All methods accept context.Context as the first
// parameter per coding guideline #5 ("Context everywhere — all blocking
// calls must take context.Context as the first argument").
type store struct {
	db *sql.DB
}

func (s *store) insertCache(ctx context.Context, r cacheRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tool_result_cache
		(id, user_name, conv_id, tool_name, args_hash, payload_path, bytes,
		 content_type, created_at, expires_at, accessed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.UserName, r.ConvID, r.ToolName, r.ArgsHash, r.PayloadPath,
		r.Bytes, r.ContentType, r.CreatedAt, r.ExpiresAt, r.AccessedAt)
	if err != nil {
		return fmt.Errorf("toolcache.insertCache: %w", err)
	}
	return nil
}

// getCacheByID enforces user ownership. Cross-user lookup returns ErrNotFound.
func (s *store) getCacheByID(ctx context.Context, user, id string) (cacheRow, error) {
	var r cacheRow
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_name, conv_id, tool_name, args_hash, payload_path,
		       bytes, content_type, created_at, expires_at, accessed_at
		  FROM tool_result_cache
		 WHERE id = ? AND user_name = ?`,
		id, user).Scan(
		&r.ID, &r.UserName, &r.ConvID, &r.ToolName, &r.ArgsHash, &r.PayloadPath,
		&r.Bytes, &r.ContentType, &r.CreatedAt, &r.ExpiresAt, &r.AccessedAt)
	if err == sql.ErrNoRows {
		return cacheRow{}, ErrNotFound
	}
	if err != nil {
		return cacheRow{}, fmt.Errorf("toolcache.getCacheByID: %w", err)
	}
	return r, nil
}

// findDedup returns the most recent unexpired row matching (user, tool, args).
// ErrNotFound when no such row exists.
func (s *store) findDedup(ctx context.Context, user, tool, argsHash string, now int64) (cacheRow, error) {
	var r cacheRow
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_name, conv_id, tool_name, args_hash, payload_path,
		       bytes, content_type, created_at, expires_at, accessed_at
		  FROM tool_result_cache
		 WHERE user_name = ? AND tool_name = ? AND args_hash = ? AND expires_at > ?
		 ORDER BY created_at DESC LIMIT 1`,
		user, tool, argsHash, now).Scan(
		&r.ID, &r.UserName, &r.ConvID, &r.ToolName, &r.ArgsHash, &r.PayloadPath,
		&r.Bytes, &r.ContentType, &r.CreatedAt, &r.ExpiresAt, &r.AccessedAt)
	if err == sql.ErrNoRows {
		return cacheRow{}, ErrNotFound
	}
	if err != nil {
		return cacheRow{}, fmt.Errorf("toolcache.findDedup: %w", err)
	}
	return r, nil
}

func (s *store) touchAccessed(ctx context.Context, id string, now int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tool_result_cache SET accessed_at = ? WHERE id = ?`, now, id)
	if err != nil {
		return fmt.Errorf("toolcache.touchAccessed: %w", err)
	}
	return nil
}

func (s *store) refreshExpiry(ctx context.Context, id string, expiresAt int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tool_result_cache SET expires_at = ? WHERE id = ?`, expiresAt, id)
	if err != nil {
		return fmt.Errorf("toolcache.refreshExpiry: %w", err)
	}
	return nil
}

func (s *store) deleteCache(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tool_result_cache WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("toolcache.deleteCache: %w", err)
	}
	return nil
}

// listExpired returns cache rows whose expires_at is past `now`.
func (s *store) listExpired(ctx context.Context, now int64) ([]cacheRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_name, conv_id, tool_name, args_hash, payload_path,
		       bytes, content_type, created_at, expires_at, accessed_at
		  FROM tool_result_cache
		 WHERE expires_at < ?`, now)
	if err != nil {
		return nil, fmt.Errorf("toolcache.listExpired: %w", err)
	}
	defer rows.Close()
	var out []cacheRow
	for rows.Next() {
		var r cacheRow
		if err := rows.Scan(&r.ID, &r.UserName, &r.ConvID, &r.ToolName, &r.ArgsHash,
			&r.PayloadPath, &r.Bytes, &r.ContentType, &r.CreatedAt, &r.ExpiresAt, &r.AccessedAt); err != nil {
			return nil, fmt.Errorf("toolcache.listExpired scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// listByUserOrderByAccessed returns all rows for a user sorted by accessed_at
// ascending (LRU = oldest accessed first). Used by per-user cap eviction.
func (s *store) listByUserOrderByAccessed(ctx context.Context, user string) ([]cacheRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_name, conv_id, tool_name, args_hash, payload_path,
		       bytes, content_type, created_at, expires_at, accessed_at
		  FROM tool_result_cache
		 WHERE user_name = ?
		 ORDER BY accessed_at ASC`, user)
	if err != nil {
		return nil, fmt.Errorf("toolcache.listByUserOrderByAccessed: %w", err)
	}
	defer rows.Close()
	var out []cacheRow
	for rows.Next() {
		var r cacheRow
		if err := rows.Scan(&r.ID, &r.UserName, &r.ConvID, &r.ToolName, &r.ArgsHash,
			&r.PayloadPath, &r.Bytes, &r.ContentType, &r.CreatedAt, &r.ExpiresAt, &r.AccessedAt); err != nil {
			return nil, fmt.Errorf("toolcache.listByUserOrderByAccessed scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// distinctUsers returns the set of user_names currently holding cache rows.
// Used by the sweeper's per-user cap pass to iterate without a full scan.
func (s *store) distinctUsers(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT user_name FROM tool_result_cache`)
	if err != nil {
		return nil, fmt.Errorf("toolcache.distinctUsers: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("toolcache.distinctUsers scan: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *store) insertAudit(ctx context.Context, a auditRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tool_result_audit
		(id, user_name, conv_id, tool_name, args_hash, args_summary, bytes,
		 content_type, category, created_at, payload_id, payload_purged_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.UserName, a.ConvID, a.ToolName, a.ArgsHash, a.ArgsSummary,
		a.Bytes, a.ContentType, a.Category, a.CreatedAt, a.PayloadID, a.PayloadPurgedAt)
	if err != nil {
		return fmt.Errorf("toolcache.insertAudit: %w", err)
	}
	return nil
}

// markAuditPayloadPurged sets payload_id=NULL + payload_purged_at on audit
// rows that referenced the given cache id. Called after a cache row is
// deleted (TTL or LRU). The audit row persists with metadata intact.
func (s *store) markAuditPayloadPurged(ctx context.Context, cacheID string, purgedAt int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tool_result_audit
		   SET payload_id = NULL, payload_purged_at = ?
		 WHERE payload_id = ?`, purgedAt, cacheID)
	if err != nil {
		return fmt.Errorf("toolcache.markAuditPayloadPurged: %w", err)
	}
	return nil
}
