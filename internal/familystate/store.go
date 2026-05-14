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
