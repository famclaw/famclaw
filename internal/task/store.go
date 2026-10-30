package task

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/famclaw/famclaw/internal/store"
)

// ErrTaskNotFound is returned when a shared-task state transition targets a
// row that does not exist or is not owned by the given caller. An UPDATE that
// matches zero rows previously returned nil (silent no-op); callers can now
// distinguish this from a genuine database failure via errors.Is.
var ErrTaskNotFound = errors.New("shared task not found or not owned by caller")

// SharedTask represents a shared task that can be assigned to a user
type SharedTask struct {
	ID          int64     `json:"id"`
	Owner       string    `json:"owner"`
	Text        string    `json:"text"`
	DueAt       time.Time `json:"due_at"`
	Source      string    `json:"source"`
	Completed   bool      `json:"completed"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Accepted    bool      `json:"accepted"`
	Declined    bool      `json:"declined"`
	HandedBack  bool      `json:"handed_back"`
}

// Store handles shared task operations
type Store struct {
	db *store.DB
}

// NewStore creates a new shared task store
func NewStore(db *store.DB) *Store {
	return &Store{db: db}
}

// AddSharedTask creates a new shared task
func (s *Store) AddSharedTask(ctx context.Context, owner, text, source string, dueAt time.Time) (*SharedTask, error) {
	now := time.Now()
	res, err := s.db.SQL().ExecContext(ctx,
		`INSERT INTO shared_tasks (owner, text, due_at, source, completed, accepted, declined, handed_back, created_at, updated_at) 
		VALUES (?, ?, ?, ?, 0, 0, 0, 0, ?, ?)`,
		owner, text, dueAt.Format(time.RFC3339), source, now.Unix(), now.Unix())
	if err != nil {
		return nil, fmt.Errorf("add shared task: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("add shared task last insert id: %w", err)
	}

	return &SharedTask{
		ID:          id,
		Owner:       owner,
		Text:        text,
		DueAt:       dueAt,
		Source:      source,
		Completed:   false,
		CreatedAt:   now,
		UpdatedAt:   now,
		Accepted:    false,
		Declined:    false,
		HandedBack:  false,
	}, nil
}

// ListSharedTasks lists all shared tasks for a user, optionally filtered by status
func (s *Store) ListSharedTasks(ctx context.Context, owner string, completed *bool, accepted *bool, declined *bool, handedBack *bool) ([]*SharedTask, error) {
	q := `SELECT id, owner, text, due_at, source, completed, created_at, updated_at, accepted, declined, handed_back FROM shared_tasks WHERE owner = ?`
	args := []any{owner}

	if completed != nil {
		q += ` AND completed = ?`
		if *completed {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}

	if accepted != nil {
		q += ` AND accepted = ?`
		if *accepted {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}

	if declined != nil {
		q += ` AND declined = ?`
		if *declined {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}

	if handedBack != nil {
		q += ` AND handed_back = ?`
		if *handedBack {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}

	q += ` ORDER BY created_at DESC`

	rows, err := s.db.SQL().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list shared tasks: %w", err)
	}
	defer rows.Close()

	var out []*SharedTask
	for rows.Next() {
		t, err := scanSharedTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan shared task: %w", err)
		}
		out = append(out, t)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list shared tasks rows: %w", err)
	}

	return out, nil
}

// AcceptTask marks a shared task as accepted by the owner. Returns
// ErrTaskNotFound (wrapped) when no row matched — i.e. the task does not
// exist or belongs to a different owner — instead of the previous silent
// no-op.
func (s *Store) AcceptTask(ctx context.Context, owner string, id int64) error {
	now := time.Now().Unix()
	res, err := s.db.SQL().ExecContext(ctx,
		`UPDATE shared_tasks SET accepted = 1, updated_at = ? WHERE id = ? AND owner = ?`,
		now, id, owner)
	if err != nil {
		return fmt.Errorf("accept task: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("accept task: %w", ErrTaskNotFound)
	}
	return nil
}

// DeclineTask marks a shared task as declined by the owner. Returns
// ErrTaskNotFound (wrapped) when no row matched — i.e. the task does not
// exist or belongs to a different owner — instead of the previous silent
// no-op.
func (s *Store) DeclineTask(ctx context.Context, owner string, id int64) error {
	now := time.Now().Unix()
	res, err := s.db.SQL().ExecContext(ctx,
		`UPDATE shared_tasks SET declined = 1, updated_at = ? WHERE id = ? AND owner = ?`,
		now, id, owner)
	if err != nil {
		return fmt.Errorf("decline task: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("decline task: %w", ErrTaskNotFound)
	}
	return nil
}

// CompleteTask marks a shared task as completed. Returns
// ErrTaskNotFound (wrapped) when no row matched — i.e. the task does not
// exist or belongs to a different owner — instead of the previous silent
// no-op.
func (s *Store) CompleteTask(ctx context.Context, owner string, id int64) error {
	now := time.Now().Unix()
	res, err := s.db.SQL().ExecContext(ctx,
		`UPDATE shared_tasks SET completed = 1, updated_at = ? WHERE id = ? AND owner = ?`,
		now, id, owner)
	if err != nil {
		return fmt.Errorf("complete task: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("complete task: %w", ErrTaskNotFound)
	}
	return nil
}

// HandBackTask marks a shared task as handed back. Returns
// ErrTaskNotFound (wrapped) when no row matched — i.e. the task does not
// exist or belongs to a different owner — instead of the previous silent
// no-op.
func (s *Store) HandBackTask(ctx context.Context, owner string, id int64) error {
	now := time.Now().Unix()
	res, err := s.db.SQL().ExecContext(ctx,
		`UPDATE shared_tasks SET handed_back = 1, updated_at = ? WHERE id = ? AND owner = ?`,
		now, id, owner)
	if err != nil {
		return fmt.Errorf("hand back task: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("hand back task: %w", ErrTaskNotFound)
	}
	return nil
}

// GetSharedTask retrieves a specific shared task by ID
func (s *Store) GetSharedTask(ctx context.Context, id int64) (*SharedTask, error) {
	row := s.db.SQL().QueryRowContext(ctx,
		`SELECT id, owner, text, due_at, source, completed, created_at, updated_at, accepted, declined, handed_back FROM shared_tasks WHERE id = ?`,
		id)
	
	return scanSharedTask(row)
}

// ListOutstandingTasks lists all outstanding shared tasks (not completed, not declined, not handed back)
func (s *Store) ListOutstandingTasks(ctx context.Context, owner string) ([]*SharedTask, error) {
	q := `SELECT id, owner, text, due_at, source, completed, created_at, updated_at, accepted, declined, handed_back 
	      FROM shared_tasks 
	      WHERE owner = ? AND completed = 0 AND declined = 0 AND handed_back = 0 
	      ORDER BY due_at ASC`
	
	rows, err := s.db.SQL().QueryContext(ctx, q, owner)
	if err != nil {
		return nil, fmt.Errorf("list outstanding tasks: %w", err)
	}
	defer rows.Close()

	var out []*SharedTask
	for rows.Next() {
		t, err := scanSharedTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan outstanding task: %w", err)
		}
		out = append(out, t)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list outstanding tasks rows: %w", err)
	}

	return out, nil
}

// scanSharedTask scans a database row into a SharedTask
func scanSharedTask(row scanner) (*SharedTask, error) {
	var t SharedTask
	var dueAtStr string
	var createdAt, updatedAt int64

	err := row.Scan(
		&t.ID, &t.Owner, &t.Text, &dueAtStr, &t.Source, &t.Completed,
		&createdAt, &updatedAt, &t.Accepted, &t.Declined, &t.HandedBack,
	)
	if err != nil {
		return nil, err
	}

	// due_at is stored as an RFC3339 TEXT string.
	t.DueAt, err = time.Parse(time.RFC3339, dueAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse due_at: %w", err)
	}

	// created_at and updated_at are stored as INTEGER Unix seconds; database/sql
	// does not convert int64 -> time.Time, so convert explicitly.
	t.CreatedAt = time.Unix(createdAt, 0)
	t.UpdatedAt = time.Unix(updatedAt, 0)

	return &t, nil
}

// scanner interface for both *sql.Row and *sql.Rows
type scanner interface {
	Scan(...interface{}) error
}