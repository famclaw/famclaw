package usermemory

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/famclaw/famclaw/internal/store"
)

// Store is the data-access layer for per-user memory.
// It wraps the project's *store.DB; it does not own the connection.
type Store struct {
	db *store.DB
}

// NewStore wraps a *store.DB. The caller is responsible for having run
// the migration (store.Open() does this automatically).
func NewStore(db *store.DB) *Store {
	return &Store{db: db}
}

// Memory is one row of user_memories.
type Memory struct {
	ID        int64
	UserName  string
	Category  string
	Label     string
	Value     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UpsertMemory inserts or updates a memory row.
// UNIQUE(user_name, category, label) determines upsert key;
// value/updated_at overwritten on conflict.
// On return, *m is fully reloaded from the persisted row.
func (s *Store) UpsertMemory(ctx context.Context, m *Memory) error {
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("upsert memory begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_memories (user_name, category, label, value, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_name, category, label) DO UPDATE SET
		  value      = excluded.value,
		  updated_at = excluded.updated_at`,
		m.UserName, m.Category, m.Label, m.Value, now, now); err != nil {
		return fmt.Errorf("upsert memory: %w", err)
	}

	// Reload the persisted row so callers see exactly what's in the DB.
	var (
		id        int64
		userName  string
		category  string
		label     string
		value     string
		createdAt int64
		updatedAt int64
	)
	if err := tx.QueryRowContext(ctx, `
		SELECT id, user_name, category, label, value, created_at, updated_at
		FROM user_memories WHERE user_name = ? AND category = ? AND label = ?`,
		m.UserName, m.Category, m.Label).Scan(
		&id, &userName, &category, &label, &value, &createdAt, &updatedAt); err != nil {
		return fmt.Errorf("upsert memory reload: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("upsert memory commit: %w", err)
	}

	m.ID = id
	m.UserName = userName
	m.Category = category
	m.Label = label
	m.Value = value
	m.CreatedAt = time.Unix(createdAt, 0)
	m.UpdatedAt = time.Unix(updatedAt, 0)
	return nil
}

// ListMemories returns memories for a user, optionally filtered by category.
// Ordered by category, label.
func (s *Store) ListMemories(ctx context.Context, userName, category string) ([]Memory, error) {
	q := `SELECT id, user_name, category, label, value, created_at, updated_at
	      FROM user_memories WHERE user_name = ?`
	var args []any = []any{userName}
	if category != "" {
		q += ` AND category = ?`
		args = append(args, category)
	}
	q += ` ORDER BY category, label`

	rows, err := s.db.SQL().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()

	var out []Memory
	for rows.Next() {
		var m Memory
		var createdAt, updatedAt int64
		if err := rows.Scan(&m.ID, &m.UserName, &m.Category, &m.Label, &m.Value, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		m.CreatedAt = time.Unix(createdAt, 0)
		m.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMemory removes one memory row by id for the given user.
// Returns nil if the row didn't exist (idempotent).
func (s *Store) DeleteMemory(ctx context.Context, userName string, id int64) error {
	if _, err := s.db.SQL().ExecContext(ctx,
		`DELETE FROM user_memories WHERE id = ? AND user_name = ?`, id, userName); err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	return nil
}

// DeleteMemoryByKey removes a memory by user_name, category, label.
// Returns sql.ErrNoRows if no rows were deleted (idempotent behavior preserved).
func (s *Store) DeleteMemoryByKey(ctx context.Context, userName, category, label string) error {
	result, err := s.db.SQL().ExecContext(ctx,
		`DELETE FROM user_memories WHERE user_name = ? AND category = ? AND label = ?`,
		userName, category, label)
	if err != nil {
		return fmt.Errorf("delete memory by key: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete memory by key rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetMemoryByKey retrieves a single memory by user_name, category, label.
// Returns sql.ErrNoRows if not found.
func (s *Store) GetMemoryByKey(ctx context.Context, userName, category, label string) (*Memory, error) {
	var m Memory
	var createdAt, updatedAt int64
	err := s.db.SQL().QueryRowContext(ctx, `
		SELECT id, user_name, category, label, value, created_at, updated_at
		FROM user_memories WHERE user_name = ? AND category = ? AND label = ?`,
		userName, category, label).Scan(
		&m.ID, &m.UserName, &m.Category, &m.Label, &m.Value, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("get memory by key: %w", err)
	}
	m.CreatedAt = time.Unix(createdAt, 0)
	m.UpdatedAt = time.Unix(updatedAt, 0)
	return &m, nil
}

// MemoryCategories returns distinct categories used by a user.
func (s *Store) MemoryCategories(ctx context.Context, userName string) ([]string, error) {
	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT DISTINCT category FROM user_memories WHERE user_name = ? ORDER BY category`, userName)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var cat string
		if err := rows.Scan(&cat); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}
		out = append(out, cat)
	}
	return out, rows.Err()
}

// scanMemory is a helper for scanning a memory row.
func scanMemory(rows *sql.Rows) (Memory, error) {
	var m Memory
	var createdAt, updatedAt int64
	if err := rows.Scan(&m.ID, &m.UserName, &m.Category, &m.Label, &m.Value, &createdAt, &updatedAt); err != nil {
		return Memory{}, err
	}
	m.CreatedAt = time.Unix(createdAt, 0)
	m.UpdatedAt = time.Unix(updatedAt, 0)
	return m, nil
}