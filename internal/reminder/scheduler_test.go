package reminder

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/store"
)

// mockSender implements gateway.Sender for testing.
type mockSender struct {
	mu   sync.Mutex
	sent []struct{ chatID, text string }
}

func (m *mockSender) Send(ctx context.Context, chatID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, struct{ chatID, text string }{chatID, text})
	return nil
}

func (m *mockSender) getSent() []struct{ chatID, text string } {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sent
}

// mockDB implements a minimal store for testing scheduler.
type mockDB struct {
	mu        sync.Mutex
	reminders []*store.Reminder
}

func newMockDB() *mockDB {
	return &mockDB{reminders: []*store.Reminder{}}
}

func (m *mockDB) CreateReminder(ctx context.Context, r *store.Reminder) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.ID = int64(len(m.reminders) + 1)
	r.CreatedAt = time.Now().UTC()
	m.reminders = append(m.reminders, r)
	return nil
}

func (m *mockDB) GetDueReminders(ctx context.Context, now time.Time) ([]*store.Reminder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*store.Reminder
	for _, r := range m.reminders {
		if !r.Dispatched && (r.DueAt.Before(now) || r.DueAt.Equal(now)) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *mockDB) GetPendingReminders(ctx context.Context) ([]*store.Reminder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*store.Reminder
	for _, r := range m.reminders {
		if !r.Dispatched {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *mockDB) MarkReminderDispatched(ctx context.Context, id int64, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.reminders {
		if r.ID == id {
			r.Dispatched = true
			r.DispatchedAt = &at
			return nil
		}
	}
	return nil
}

func TestDispatcher(t *testing.T) {
	d := NewDispatcher()
	ms := &mockSender{}
	d.RegisterSender("telegram", ms)

	ctx := context.Background()
	r := &store.Reminder{
		ID:         1,
		UserName:   "alice",
		Gateway:    "telegram",
		ExternalID: "123",
		Message:    "take out trash",
		DueAt:      time.Now().UTC(),
	}

	err := d.Dispatch(ctx, r)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	sent := ms.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent, got %d", len(sent))
	}
	if sent[0].chatID != "123" {
		t.Errorf("chatID = %q, want %q", sent[0].chatID, "123")
	}
	expectedText := "⏰ Reminder: take out trash"
	if sent[0].text != expectedText {
		t.Errorf("text = %q, want %q", sent[0].text, expectedText)
	}
}

func TestDispatcherGroupChat(t *testing.T) {
	d := NewDispatcher()
	ms := &mockSender{}
	d.RegisterSender("telegram", ms)

	ctx := context.Background()
	r := &store.Reminder{
		ID:         1,
		UserName:   "alice",
		Gateway:    "telegram",
		ExternalID: "123",
		GroupID:    "456",
		IsGroup:    true,
		Message:    "group reminder",
		DueAt:      time.Now().UTC(),
	}

	err := d.Dispatch(ctx, r)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	sent := ms.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent, got %d", len(sent))
	}
	// Should use groupID for group chats
	if sent[0].chatID != "456" {
		t.Errorf("chatID = %q, want %q (groupID)", sent[0].chatID, "456")
	}
}

func TestDispatcherNoSender(t *testing.T) {
	d := NewDispatcher()
	// No sender registered for "discord"

	ctx := context.Background()
	r := &store.Reminder{
		ID:         1,
		UserName:   "alice",
		Gateway:    "discord",
		ExternalID: "123",
		Message:    "test",
		DueAt:      time.Now().UTC(),
	}

	// Should not error, just silently skip
	err := d.Dispatch(ctx, r)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestSchedulerProcessDue(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	ms := &mockSender{}
	dispatcher.RegisterSender("telegram", ms)

	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })

	// Add a due reminder
	ctx := context.Background()
	err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "alice",
		Gateway:    "telegram",
		ExternalID: "123",
		Message:    "take out trash",
		DueAt:      clock.Add(-1 * time.Minute),
		Dispatched: false,
	})
	if err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	// Add a future reminder (should not be dispatched)
	err = db.CreateReminder(ctx, &store.Reminder{
		UserName:   "bob",
		Gateway:    "telegram",
		ExternalID: "456",
		Message:    "future reminder",
		DueAt:      clock.Add(1 * time.Hour),
		Dispatched: false,
	})
	if err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	// Run once
	s.processDue(ctx)

	// Give a moment for async dispatch
	time.Sleep(50 * time.Millisecond)

	sent := ms.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 dispatched, got %d", len(sent))
	}
	if sent[0].text != "⏰ Reminder: take out trash" {
		t.Errorf("wrong text: %q", sent[0].text)
	}
}

func TestSchedulerReschedulePending(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	ms := &mockSender{}
	dispatcher.RegisterSender("telegram", ms)

	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })

	// Add a past-due reminder (simulating reminder from before restart)
	ctx := context.Background()
	err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "alice",
		Gateway:    "telegram",
		ExternalID: "123",
		Message:    "overdue reminder",
		DueAt:      clock.Add(-1 * time.Hour),
		Dispatched: false,
	})
	if err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	// Reschedule on startup
	s.ReschedulePending(ctx)

	// Give a moment for async dispatch
	time.Sleep(50 * time.Millisecond)

	sent := ms.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 dispatched, got %d", len(sent))
	}
	if sent[0].text != "⏰ Reminder: overdue reminder" {
		t.Errorf("wrong text: %q", sent[0].text)
	}
}

func TestSchedulerFutureRemindersNotDispatched(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	ms := &mockSender{}
	dispatcher.RegisterSender("telegram", ms)

	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })

	ctx := context.Background()
	// Add only future reminders
	err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "alice",
		Gateway:    "telegram",
		ExternalID: "123",
		Message:    "future reminder",
		DueAt:      clock.Add(1 * time.Hour),
		Dispatched: false,
	})
	if err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	s.processDue(ctx)
	time.Sleep(50 * time.Millisecond)

	sent := ms.getSent()
	if len(sent) != 0 {
		t.Errorf("expected 0 dispatched, got %d", len(sent))
	}
}
