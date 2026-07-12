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

// UpsertCategory inserts a new custom category or updates an existing custom
// category. Built-in categories (is_builtin=1) are immutable from this entry
// point — attempting to upsert one returns ErrBuiltinCategory so the
// always_inject safety contract on `allergies` / `dietary_restrictions`
// cannot be flipped off by a caller. The seed migration is the only
// authoritative source for built-in rows.
//
// Atomic: the existence-and-builtin check and the upsert run in a single
// transaction so a concurrent writer cannot promote a row to is_builtin=1
// between the two statements.
func (s *Store) UpsertCategory(ctx context.Context, c *Category) error {
	now := time.Now().Unix()
	alwaysInject := 0
	if c.AlwaysInject {
		alwaysInject = 1
	}

	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("upsert category begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var isBuiltin int
	switch err := tx.QueryRowContext(ctx,
		`SELECT is_builtin FROM family_fact_categories WHERE name = ?`, c.Name).Scan(&isBuiltin); {
	case err == sql.ErrNoRows:
		// new row — insert below
	case err != nil:
		return fmt.Errorf("upsert category lookup: %w", err)
	default:
		if isBuiltin == 1 {
			return ErrBuiltinCategory
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO family_fact_categories (name, description, always_inject, is_builtin, created_at, updated_at)
		 VALUES (?, ?, ?, 0, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   description   = excluded.description,
		   always_inject = excluded.always_inject,
		   updated_at    = excluded.updated_at
		 WHERE family_fact_categories.is_builtin = 0`,
		c.Name, c.Description, alwaysInject, now, now); err != nil {
		return fmt.Errorf("upsert category: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("upsert category commit: %w", err)
	}
	return nil
}

// DeleteCategory removes a custom category. Built-in categories cannot be
// deleted (ErrBuiltinCategory). A category with at least one referencing
// fact cannot be deleted (ErrCategoryNotEmpty). The schema declares
// FK RESTRICT, but modernc.org/sqlite does not honor _fk=true in the DSN
// (project-wide foreign_keys pragma is 0), so the constraint is enforced
// in the application layer here.
//
// Atomic: lookup, reference count, and DELETE all run in a single
// transaction so a concurrent UpsertFact cannot slip a referencing row in
// between the count and the delete.
func (s *Store) DeleteCategory(ctx context.Context, name string) error {
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete category begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var isBuiltin int
	err = tx.QueryRowContext(ctx,
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

	var refs int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM family_facts WHERE category = ?`, name).Scan(&refs); err != nil {
		return fmt.Errorf("delete category ref count: %w", err)
	}
	if refs > 0 {
		return ErrCategoryNotEmpty
	}

	res, err := tx.ExecContext(ctx,
		`DELETE FROM family_fact_categories WHERE name = ? AND is_builtin = 0`, name)
	if err != nil {
		return fmt.Errorf("delete category: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUnknownCategory
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete category commit: %w", err)
	}
	return nil
}

// UpsertFact inserts or updates a fact row. The category must exist
// (ErrUnknownCategory if not). UNIQUE(category, subject, label) determines
// upsert key; value/recurrence/updated_at overwritten on conflict.
// created_by and created_at are set on insert and preserved on update.
//
// On return, *f is fully reloaded from the persisted row — so on the
// conflict-update path, f.CreatedAt and f.CreatedBy reflect the original
// insert (not the caller's input or `now`).
//
// Atomic: the category existence check and the upsert+reload run in a
// single transaction so a concurrent DeleteCategory cannot remove the
// parent row between the check and the insert.
func (s *Store) UpsertFact(ctx context.Context, f *Fact) error {
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("upsert fact begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx,
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
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO family_facts (category, subject, label, value, recurrence, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(category, subject, label) DO UPDATE SET
		   value      = excluded.value,
		   recurrence = excluded.recurrence,
		   updated_at = excluded.updated_at`,
		f.Category, f.Subject, f.Label, f.Value, rec, f.CreatedBy, now, now); err != nil {
		return fmt.Errorf("upsert fact: %w", err)
	}

	// Reload the persisted row so callers see exactly what's in the DB —
	// on the conflict-update path, created_at and created_by come from
	// the original insert, not the input or `now`.
	var (
		recurrence                                 sql.NullString
		createdAt, updatedAt                       int64
		id                                         int64
		category, subject, label, value, createdBy string
	)
	if err := tx.QueryRowContext(ctx,
		`SELECT id, category, subject, label, value, recurrence, created_by, created_at, updated_at
		 FROM family_facts WHERE category = ? AND subject = ? AND label = ?`,
		f.Category, f.Subject, f.Label).Scan(
		&id, &category, &subject, &label, &value, &recurrence, &createdBy, &createdAt, &updatedAt); err != nil {
		return fmt.Errorf("upsert fact reload: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("upsert fact commit: %w", err)
	}

	f.ID = id
	f.Category = category
	f.Subject = subject
	f.Label = label
	f.Value = value
	if recurrence.Valid {
		f.Recurrence = recurrence.String
	} else {
		f.Recurrence = ""
	}
	f.CreatedBy = createdBy
	f.CreatedAt = time.Unix(createdAt, 0)
	f.UpdatedAt = time.Unix(updatedAt, 0)
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
