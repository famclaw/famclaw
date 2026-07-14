package todo

import (
	"context"

	"github.com/famclaw/famclaw/internal/store"
)

// Store wraps the project's *store.DB for todo operations.
type Store struct {
	db *store.DB
}

// NewStore creates a new todo Store.
func NewStore(db *store.DB) *Store {
	return &Store{db: db}
}

// AddTodo adds a new todo for the user.
func (s *Store) AddTodo(ctx context.Context, userName, text string) (*store.Todo, error) {
	return s.db.AddTodo(ctx, userName, text)
}

// ListTodos lists todos for the user, optionally filtered by completion status.
func (s *Store) ListTodos(ctx context.Context, userName string, completed *bool) ([]*store.Todo, error) {
	return s.db.ListTodos(ctx, userName, completed)
}

// CompleteTodo marks a todo as completed.
func (s *Store) CompleteTodo(ctx context.Context, userName string, id int64) error {
	return s.db.CompleteTodo(ctx, userName, id)
}

// UncompleteTodo marks a todo as not completed (reopen).
func (s *Store) UncompleteTodo(ctx context.Context, userName string, id int64) error {
	return s.db.UncompleteTodo(ctx, userName, id)
}

// RemoveTodo deletes a todo.
func (s *Store) RemoveTodo(ctx context.Context, userName string, id int64) error {
	return s.db.RemoveTodo(ctx, userName, id)
}