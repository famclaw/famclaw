package store

import (
	"context"
	"testing"
	"time"
)

func TestTodoCRUD(t *testing.T) {
	tests := []struct {
		name string
		fn   func(t *testing.T, db *DB, ctx context.Context)
	}{
		{
			name: "AddTodo",
			fn: func(t *testing.T, db *DB, ctx context.Context) {
				todo, err := db.AddTodo(ctx, "alice", "buy milk")
				if err != nil {
					t.Fatalf("AddTodo: %v", err)
				}
				if todo.ID == 0 {
					t.Error("expected non-zero ID")
				}
				if todo.UserName != "alice" {
					t.Errorf("UserName = %q, want alice", todo.UserName)
				}
				if todo.Text != "buy milk" {
					t.Errorf("Text = %q, want buy milk", todo.Text)
				}
				if todo.Completed {
					t.Error("expected Completed = false")
				}
				if todo.CreatedAt.IsZero() {
					t.Error("expected non-zero CreatedAt")
				}
				if todo.UpdatedAt.IsZero() {
					t.Error("expected non-zero UpdatedAt")
				}
			},
		},
		{
			name: "ListTodos",
			fn: func(t *testing.T, db *DB, ctx context.Context) {
				// Add a few todos for alice
				_, _ = db.AddTodo(ctx, "alice", "buy milk")
				_, _ = db.AddTodo(ctx, "alice", "walk dog")
				_, _ = db.AddTodo(ctx, "bob", "read book")

				// List all for alice
				todos, err := db.ListTodos(ctx, "alice", nil)
				if err != nil {
					t.Fatalf("ListTodos: %v", err)
				}
				if len(todos) != 2 {
					t.Errorf("expected 2 todos for alice, got %d", len(todos))
				}

				// List active for alice (default filter)
				todos, err = db.ListTodos(ctx, "alice", func() *bool { b := false; return &b }())
				if err != nil {
					t.Fatalf("ListTodos active: %v", err)
				}
				if len(todos) != 2 {
					t.Errorf("expected 2 active todos for alice, got %d", len(todos))
				}

				// List completed for alice (should be 0)
				todos, err = db.ListTodos(ctx, "alice", func() *bool { b := true; return &b }())
				if err != nil {
					t.Fatalf("ListTodos completed: %v", err)
				}
				if len(todos) != 0 {
					t.Errorf("expected 0 completed todos for alice, got %d", len(todos))
				}

				// List all for bob
				todos, err = db.ListTodos(ctx, "bob", nil)
				if err != nil {
					t.Fatalf("ListTodos bob: %v", err)
				}
				if len(todos) != 1 {
					t.Errorf("expected 1 todo for bob, got %d", len(todos))
				}
				if todos[0].Text != "read book" {
					t.Errorf("bob's todo = %q, want read book", todos[0].Text)
				}
			},
		},
		{
			name: "CompleteTodo",
			fn: func(t *testing.T, db *DB, ctx context.Context) {
				// Add a todo for alice
				todo, err := db.AddTodo(ctx, "alice", "buy eggs")
				if err != nil {
					t.Fatalf("AddTodo: %v", err)
				}

				// Complete it
				err = db.CompleteTodo(ctx, "alice", todo.ID)
				if err != nil {
					t.Fatalf("CompleteTodo: %v", err)
				}

				// Verify it's completed
				todos, err := db.ListTodos(ctx, "alice", func() *bool { b := true; return &b }())
				if err != nil {
					t.Fatalf("ListTodos completed: %v", err)
				}
				if len(todos) != 1 {
					t.Errorf("expected 1 completed todo, got %d", len(todos))
				}
				if todos[0].ID != todo.ID {
					t.Errorf("completed todo ID = %d, want %d", todos[0].ID, todo.ID)
				}
				if !todos[0].Completed {
					t.Error("expected completed = true")
				}

				// Try to complete again (should still work - idempotent)
				err = db.CompleteTodo(ctx, "alice", todo.ID)
				if err != nil {
					t.Fatalf("CompleteTodo again: %v", err)
				}

				// Try to complete non-existent todo
				err = db.CompleteTodo(ctx, "alice", 9999)
				if err == nil {
					t.Error("expected error for non-existent todo")
				}

				// Try to complete another user's todo
				_, _ = db.AddTodo(ctx, "bob", "bob's task")
				err = db.CompleteTodo(ctx, "alice", 9999)
				if err == nil {
					t.Error("expected error for another user's todo")
				}
			},
		},
		{
			name: "RemoveTodo",
			fn: func(t *testing.T, db *DB, ctx context.Context) {
				// Add a todo
				todo, err := db.AddTodo(ctx, "alice", "remove me")
				if err != nil {
					t.Fatalf("AddTodo: %v", err)
				}

				// Remove it
				err = db.RemoveTodo(ctx, "alice", todo.ID)
				if err != nil {
					t.Fatalf("RemoveTodo: %v", err)
				}

				// Verify it's gone
				todos, err := db.ListTodos(ctx, "alice", nil)
				if err != nil {
					t.Fatalf("ListTodos: %v", err)
				}
				for _, td := range todos {
					if td.ID == todo.ID {
						t.Error("removed todo still appears in list")
					}
				}

				// Try to remove non-existent
				err = db.RemoveTodo(ctx, "alice", 9999)
				if err == nil {
					t.Error("expected error for non-existent todo")
				}

				// Try to remove another user's todo
				_, _ = db.AddTodo(ctx, "bob", "bob's task")
				err = db.RemoveTodo(ctx, "alice", 9999)
				if err == nil {
					t.Error("expected error for another user's todo")
				}
			},
		},
		{
			name: "UncompleteTodo",
			fn: func(t *testing.T, db *DB, ctx context.Context) {
				// Add and complete a todo
				todo, err := db.AddTodo(ctx, "alice", "reopen me")
				if err != nil {
					t.Fatalf("AddTodo: %v", err)
				}
				err = db.CompleteTodo(ctx, "alice", todo.ID)
				if err != nil {
					t.Fatalf("CompleteTodo: %v", err)
				}

				// Uncomplete it
				err = db.UncompleteTodo(ctx, "alice", todo.ID)
				if err != nil {
					t.Fatalf("UncompleteTodo: %v", err)
				}

				// Verify it's active again
				todos, err := db.ListTodos(ctx, "alice", func() *bool { b := false; return &b }())
				if err != nil {
					t.Fatalf("ListTodos active: %v", err)
				}
				found := false
				for _, td := range todos {
					if td.ID == todo.ID {
						found = true
						if td.Completed {
							t.Error("expected completed = false after uncomplete")
						}
					}
				}
				if !found {
					t.Error("uncompleted todo not found in active list")
				}
			},
		},
		{
			name: "ListTodosWithStringFilter",
			fn: func(t *testing.T, db *DB, ctx context.Context) {
				// Test the underlying DB method directly with different filter values
				_, _ = db.AddTodo(ctx, "carol", "task 1")
				_, _ = db.AddTodo(ctx, "carol", "task 2")
				// Complete the first one (note: IDs may differ, get the actual ID)
				todos, _ := db.ListTodos(ctx, "carol", nil)
				if len(todos) > 0 {
					db.CompleteTodo(ctx, "carol", todos[0].ID)
				}

				// Test all
				todos, err := db.ListTodos(ctx, "carol", nil)
				if err != nil {
					t.Fatalf("ListTodos all: %v", err)
				}
				if len(todos) != 2 {
					t.Errorf("expected 2 todos (all), got %d", len(todos))
				}

				// Test active (completed=false)
				activeFalse := false
				todos, err = db.ListTodos(ctx, "carol", &activeFalse)
				if err != nil {
					t.Fatalf("ListTodos active: %v", err)
				}
				for _, td := range todos {
					if td.Completed {
						t.Error("found completed todo in active list")
					}
				}

				// Test completed (completed=true)
				completedTrue := true
				todos, err = db.ListTodos(ctx, "carol", &completedTrue)
				if err != nil {
					t.Fatalf("ListTodos completed: %v", err)
				}
				for _, td := range todos {
					if !td.Completed {
						t.Error("found active todo in completed list")
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := Open(":memory:")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer db.Close()

			ctx := context.Background()
			tt.fn(t, db, ctx)
		})
	}
}

func TestTodoOrdering(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Add todos with delay to ensure different timestamps (Unix time in seconds)
	_, _ = db.AddTodo(ctx, "alice", "first")
	time.Sleep(1100 * time.Millisecond)
	_, _ = db.AddTodo(ctx, "alice", "second")
	time.Sleep(1100 * time.Millisecond)
	_, _ = db.AddTodo(ctx, "alice", "third")

	// List should return in order: incomplete first (by created_at DESC), then completed
	todos, err := db.ListTodos(ctx, "alice", nil)
	if err != nil {
		t.Fatalf("ListTodos: %v", err)
	}
	if len(todos) != 3 {
		t.Fatalf("expected 3 todos, got %d", len(todos))
	}
	// Most recent first (created_at DESC for incomplete)
	if todos[0].Text != "third" {
		t.Errorf("first todo = %q, want third", todos[0].Text)
	}
	if todos[1].Text != "second" {
		t.Errorf("second todo = %q, want second", todos[1].Text)
	}
	if todos[2].Text != "first" {
		t.Errorf("third todo = %q, want first", todos[2].Text)
	}

	// Complete the middle one (which is "second" at index 1)
	err = db.CompleteTodo(ctx, "alice", todos[1].ID)
	if err != nil {
		t.Fatalf("CompleteTodo: %v", err)
	}

	// List again - completed should come last
	todos, err = db.ListTodos(ctx, "alice", nil)
	if err != nil {
		t.Fatalf("ListTodos: %v", err)
	}
	if len(todos) != 3 {
		t.Fatalf("expected 3 todos, got %d", len(todos))
	}
	// First two should be incomplete (third, first), last should be completed (second)
	if todos[0].Text != "third" {
		t.Errorf("first = %q, want third", todos[0].Text)
	}
	if todos[1].Text != "first" {
		t.Errorf("second = %q, want first", todos[1].Text)
	}
	if todos[2].Text != "second" {
		t.Errorf("third = %q, want second", todos[2].Text)
	}
	if !todos[2].Completed {
		t.Error("expected last todo to be completed")
	}
}