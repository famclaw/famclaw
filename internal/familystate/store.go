package familystate

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/famclaw/famclaw/internal/store"
)

// Store is the data-access layer for family state. It wraps the project's
// *store.DB; it does not own the connection (callers manage Open/Close).
type Store struct {
	db *store.DB
}

// NewStore wraps a *store.DB. The caller is responsible for having run
// the migration (Open() does this automatically).
func NewStore(db *store.DB) *Store {
	return &Store{db: db}
}

// Category is one row of family_fact_categories.
type Category struct {
	Name         string
	Description  string
	AlwaysInject bool
	IsBuiltin    bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Fact is one row of family_facts.
type Fact struct {
	ID         int64
	Category   string
	Subject    string
	Label      string
	Value      string
	Recurrence string // empty == NULL in DB
	CreatedBy  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ListCategories returns every row in family_fact_categories ordered by name.
func (s *Store) ListCategories(ctx context.Context) ([]Category, error) {
	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT name, description, always_inject, is_builtin, created_at, updated_at
		 FROM family_fact_categories ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	defer rows.Close()

	var out []Category
	for rows.Next() {
		var c Category
		var alwaysInject, isBuiltin int
		var createdAt, updatedAt int64
		if err := rows.Scan(&c.Name, &c.Description, &alwaysInject, &isBuiltin, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}
		c.AlwaysInject = alwaysInject == 1
		c.IsBuiltin = isBuiltin == 1
		c.CreatedAt = time.Unix(createdAt, 0)
		c.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate categories: %w", err)
	}
	return out, nil
}

// FilterOpts narrows ListFacts. Zero-value lists every row.
type FilterOpts struct {
	Category string // exact match if set
	Subject  string // exact match if set
}

// UpsertCategory inserts or updates a row in family_fact_categories. is_builtin
// cannot be changed by callers — the seed migration is the only authoritative
// source. Description and always_inject ARE overwritten on conflict (this is
// the "parent edits a custom category" path).
func (s *Store) UpsertCategory(ctx context.Context, c *Category) error {
	now := time.Now().Unix()
	alwaysInject := 0
	if c.AlwaysInject {
		alwaysInject = 1
	}
	// New rows always get is_builtin=0; existing rows keep their current value.
	if _, err := s.db.SQL().ExecContext(ctx,
		`INSERT INTO family_fact_categories (name, description, always_inject, is_builtin, created_at, updated_at)
		 VALUES (?, ?, ?, 0, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   description   = excluded.description,
		   always_inject = excluded.always_inject,
		   updated_at    = excluded.updated_at`,
		c.Name, c.Description, alwaysInject, now, now); err != nil {
		return fmt.Errorf("upsert category: %w", err)
	}
	return nil
}

// DeleteCategory removes a custom category. Built-in categories cannot be
// deleted (ErrBuiltinCategory). A category with at least one referencing
// fact cannot be deleted (ErrCategoryNotEmpty — FK RESTRICT).
func (s *Store) DeleteCategory(ctx context.Context, name string) error {
	var isBuiltin int
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT is_builtin FROM family_fact_categories WHERE name = ?`, name).Scan(&isBuiltin)
	if err == sql.ErrNoRows {
		return ErrUnknownCategory
	}
	if err != nil {
		return fmt.Errorf("delete category lookup: %w", err)
	}
	if isBuiltin == 1 {
		return ErrBuiltinCategory
	}
	// Explicit count check: the schema declares FK RESTRICT, but modernc.org/sqlite
	// does not honor _fk=true in the DSN (the project-wide foreign_keys pragma is 0),
	// so we enforce the constraint at the application layer.
	var refs int
	if err := s.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(1) FROM family_facts WHERE category = ?`, name).Scan(&refs); err != nil {
		return fmt.Errorf("delete category ref count: %w", err)
	}
	if refs > 0 {
		return ErrCategoryNotEmpty
	}
	res, err := s.db.SQL().ExecContext(ctx,
		`DELETE FROM family_fact_categories WHERE name = ? AND is_builtin = 0`, name)
	if err != nil {
		return fmt.Errorf("delete category: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUnknownCategory
	}
	return nil
}

// UpsertFact inserts or updates a fact row. The category must exist (FK).
// UNIQUE(category, subject, label) determines upsert key; value/recurrence/
// updated_at overwritten on conflict. CreatedBy is set only on insert; it is
// preserved across updates.
func (s *Store) UpsertFact(ctx context.Context, f *Fact) error {
	// Pre-flight: better error message than the FK driver text.
	var exists int
	if err := s.db.SQL().QueryRowContext(ctx,
		`SELECT 1 FROM family_fact_categories WHERE name = ?`, f.Category).Scan(&exists); err == sql.ErrNoRows {
		return ErrUnknownCategory
	} else if err != nil {
		return fmt.Errorf("upsert fact category check: %w", err)
	}

	now := time.Now().Unix()
	var rec sql.NullString
	if f.Recurrence != "" {
		rec = sql.NullString{String: f.Recurrence, Valid: true}
	}
	res, err := s.db.SQL().ExecContext(ctx,
		`INSERT INTO family_facts (category, subject, label, value, recurrence, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(category, subject, label) DO UPDATE SET
		   value      = excluded.value,
		   recurrence = excluded.recurrence,
		   updated_at = excluded.updated_at`,
		f.Category, f.Subject, f.Label, f.Value, rec, f.CreatedBy, now, now)
	if err != nil {
		return fmt.Errorf("upsert fact: %w", err)
	}
	if id, _ := res.LastInsertId(); id != 0 {
		f.ID = id
	} else {
		if err := s.db.SQL().QueryRowContext(ctx,
			`SELECT id FROM family_facts WHERE category=? AND subject=? AND label=?`,
			f.Category, f.Subject, f.Label).Scan(&f.ID); err != nil {
			return fmt.Errorf("upsert fact reselect: %w", err)
		}
	}
	f.CreatedAt = time.Unix(now, 0)
	f.UpdatedAt = time.Unix(now, 0)
	return nil
}

// ListFacts returns facts ordered by category, subject, label.
func (s *Store) ListFacts(ctx context.Context, opts FilterOpts) ([]Fact, error) {
	q := `SELECT id, category, subject, label, value, recurrence, created_by, created_at, updated_at
	      FROM family_facts WHERE 1=1`
	var args []any
	if opts.Category != "" {
		q += ` AND category = ?`
		args = append(args, opts.Category)
	}
	if opts.Subject != "" {
		q += ` AND subject = ?`
		args = append(args, opts.Subject)
	}
	q += ` ORDER BY category, subject, label`

	rows, err := s.db.SQL().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list facts: %w", err)
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, fmt.Errorf("scan fact: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteFact removes one fact row by id. Returns nil if the row didn't exist.
func (s *Store) DeleteFact(ctx context.Context, id int64) error {
	if _, err := s.db.SQL().ExecContext(ctx,
		`DELETE FROM family_facts WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete fact: %w", err)
	}
	return nil
}

// scanFact is shared by ListFacts and any future single-row reader.
func scanFact(rows *sql.Rows) (Fact, error) {
	var f Fact
	var recurrence sql.NullString
	var createdAt, updatedAt int64
	if err := rows.Scan(&f.ID, &f.Category, &f.Subject, &f.Label, &f.Value,
		&recurrence, &f.CreatedBy, &createdAt, &updatedAt); err != nil {
		return Fact{}, err
	}
	if recurrence.Valid {
		f.Recurrence = recurrence.String
	}
	f.CreatedAt = time.Unix(createdAt, 0)
	f.UpdatedAt = time.Unix(updatedAt, 0)
	return f, nil
}
