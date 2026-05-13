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
// the migration (store.Open does this automatically).
func NewStore(db *store.DB) *Store {
	return &Store{db: db}
}

// Category is one row of family_fact_categories.
type Category struct {
	Name         string
	Description  string
	AlwaysInject bool
	IsBuiltin    bool
	CreatedAt    int64
	UpdatedAt    int64
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
	CreatedAt  int64
	UpdatedAt  int64
}

// FilterOpts narrows ListFacts. Zero-value lists every row.
type FilterOpts struct {
	Subject  string // exact match if set
	Category string // exact match if set
}

// ListCategories returns every row in family_fact_categories ordered by name.
func (s *Store) ListCategories(ctx context.Context) ([]Category, error) {
	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT name, description, always_inject, is_builtin, created_at, updated_at
		 FROM family_fact_categories ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("familystate.ListCategories: %w", err)
	}
	defer rows.Close()

	var out []Category
	for rows.Next() {
		var c Category
		var alwaysInject, isBuiltin int
		if err := rows.Scan(&c.Name, &c.Description, &alwaysInject, &isBuiltin, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("familystate.ListCategories: %w", err)
		}
		c.AlwaysInject = alwaysInject == 1
		c.IsBuiltin = isBuiltin == 1
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("familystate.ListCategories: %w", err)
	}
	return out, nil
}

// scanFact scans a single row from family_facts into a Fact. Recurrence is
// nullable in the DB; a NULL recurrence is returned as an empty string.
func scanFact(rows *sql.Rows) (*Fact, error) {
	var f Fact
	var recurrence sql.NullString
	if err := rows.Scan(&f.ID, &f.Category, &f.Subject, &f.Label, &f.Value,
		&recurrence, &f.CreatedBy, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return nil, err
	}
	if recurrence.Valid {
		f.Recurrence = recurrence.String
	}
	return &f, nil
}

// UpsertCategory inserts or updates a row in family_fact_categories.
// is_builtin cannot be changed by callers — the seed migration is the only
// authoritative source. Description and always_inject ARE overwritten on
// conflict (this is the "parent edits a custom category" path).
// Returns ErrBuiltinCategory if a caller tries to upsert over an existing
// builtin row. The Category is mutated in place: CreatedAt and UpdatedAt
// are populated on return.
func (s *Store) UpsertCategory(ctx context.Context, c *Category) error {
	now := time.Now().Unix()
	alwaysInject := 0
	if c.AlwaysInject {
		alwaysInject = 1
	}

	// Pre-flight: if a row already exists and is builtin, reject.
	var isBuiltin int
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT is_builtin FROM family_fact_categories WHERE name = ?`, c.Name).Scan(&isBuiltin)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("familystate.UpsertCategory: %w", err)
	}
	if err == nil && isBuiltin == 1 {
		return fmt.Errorf("familystate.UpsertCategory: %w", ErrBuiltinCategory)
	}

	// New rows always get is_builtin=0; existing non-builtin rows are updated.
	if _, err := s.db.SQL().ExecContext(ctx,
		`INSERT INTO family_fact_categories (name, description, always_inject, is_builtin, created_at, updated_at)
		 VALUES (?, ?, ?, 0, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   description   = excluded.description,
		   always_inject = excluded.always_inject,
		   updated_at    = excluded.updated_at`,
		c.Name, c.Description, alwaysInject, now, now); err != nil {
		return fmt.Errorf("familystate.UpsertCategory: %w", err)
	}
	c.CreatedAt = now
	c.UpdatedAt = now
	return nil
}

// DeleteCategory removes a custom category. Built-in categories cannot be
// deleted (ErrBuiltinCategory). A category with at least one referencing fact
// cannot be deleted (ErrCategoryNotEmpty). Returns nil if the category did not
// exist (idempotent).
func (s *Store) DeleteCategory(ctx context.Context, name string) error {
	var isBuiltin int
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT is_builtin FROM family_fact_categories WHERE name = ?`, name).Scan(&isBuiltin)
	if err == sql.ErrNoRows {
		// Category doesn't exist — nothing to delete.
		return nil
	}
	if err != nil {
		return fmt.Errorf("familystate.DeleteCategory: %w", err)
	}
	if isBuiltin == 1 {
		return fmt.Errorf("familystate.DeleteCategory: %w", ErrBuiltinCategory)
	}

	// Explicit count check: modernc.org/sqlite does not surface FK RESTRICT
	// errors consistently (the _fk=true DSN parameter has no effect; FK
	// enforcement requires a PRAGMA foreign_keys = ON per-connection). We
	// guard here in application code to guarantee the semantics regardless.
	var factCount int
	if err := s.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM family_facts WHERE category = ?`, name).Scan(&factCount); err != nil {
		return fmt.Errorf("familystate.DeleteCategory: %w", err)
	}
	if factCount > 0 {
		return fmt.Errorf("familystate.DeleteCategory: %w", ErrCategoryNotEmpty)
	}

	if _, err = s.db.SQL().ExecContext(ctx,
		`DELETE FROM family_fact_categories WHERE name = ? AND is_builtin = 0`, name); err != nil {
		return fmt.Errorf("familystate.DeleteCategory: %w", err)
	}
	return nil
}

// UpsertFact inserts or updates a fact row. The category must exist.
// UNIQUE(category, subject, label) determines the upsert key; value, recurrence,
// and updated_at are overwritten on conflict. CreatedBy is set only on insert;
// it is preserved across updates. The Fact is mutated in place: ID, CreatedAt,
// and UpdatedAt are populated on return.
func (s *Store) UpsertFact(ctx context.Context, f *Fact) error {
	// Pre-flight: emit a better error than the raw FK driver text.
	var exists int
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT 1 FROM family_fact_categories WHERE name = ?`, f.Category).Scan(&exists)
	if err == sql.ErrNoRows {
		return fmt.Errorf("familystate.UpsertFact: %w", ErrUnknownCategory)
	}
	if err != nil {
		return fmt.Errorf("familystate.UpsertFact: %w", err)
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
		return fmt.Errorf("familystate.UpsertFact: %w", err)
	}

	// modernc.org/sqlite returns LastInsertId > 0 on INSERT, 0 on ON CONFLICT UPDATE.
	if id, _ := res.LastInsertId(); id != 0 {
		f.ID = id
	} else {
		// UPDATE branch: re-SELECT by the unique key to obtain the row id.
		if err := s.db.SQL().QueryRowContext(ctx,
			`SELECT id FROM family_facts WHERE category = ? AND subject = ? AND label = ?`,
			f.Category, f.Subject, f.Label).Scan(&f.ID); err != nil {
			return fmt.Errorf("familystate.UpsertFact reselect: %w", err)
		}
	}
	f.CreatedAt = now
	f.UpdatedAt = now
	return nil
}

// ListFacts returns facts ordered by subject, category, label.
// FilterOpts.Category and FilterOpts.Subject each constrain results when set.
func (s *Store) ListFacts(ctx context.Context, opts FilterOpts) ([]*Fact, error) {
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
	q += ` ORDER BY subject, category, label`

	rows, err := s.db.SQL().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("familystate.ListFacts: %w", err)
	}
	defer rows.Close()

	var out []*Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, fmt.Errorf("familystate.ListFacts: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("familystate.ListFacts: %w", err)
	}
	return out, nil
}

// DeleteFact removes one fact row by id. Returns nil if the row didn't exist.
func (s *Store) DeleteFact(ctx context.Context, id int64) error {
	if _, err := s.db.SQL().ExecContext(ctx,
		`DELETE FROM family_facts WHERE id = ?`, id); err != nil {
		return fmt.Errorf("familystate.DeleteFact: %w", err)
	}
	return nil
}
