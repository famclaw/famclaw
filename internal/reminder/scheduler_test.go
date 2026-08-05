package reminder

import (
	"context"
	"errors"
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

// failSender is a gateway.Sender whose Send always fails with a permanent
// "unknown channel" error, simulating a Discord channel that 404s. Used to
// verify the scheduler gives up instead of retrying forever.
type failSender struct {
	mu    sync.Mutex
	calls int
}

func (f *failSender) Send(ctx context.Context, chatID, text string) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return errors.New("unknown channel")
}

// mockDB implements a minimal store for testing scheduler.
type mockDB struct {
	mu               sync.Mutex
	reminders        []*store.Reminder
	deliveryAttempts map[int64]int
	incrementErr     error // if non-nil, IncrementDeliveryAttempt returns this error
}

func newMockDB() *mockDB {
	return &mockDB{reminders: []*store.Reminder{}, deliveryAttempts: map[int64]int{}}
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

func (m *mockDB) IncrementDeliveryAttempt(ctx context.Context, id int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.incrementErr != nil {
		return 0, m.incrementErr
	}
	m.deliveryAttempts[id]++
	return m.deliveryAttempts[id], nil
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

func TestSchedulerStop(t *testing.T) {
	db := newMockDB()
	dispatcher := NewDispatcher()
	s := NewScheduler(db, dispatcher, 10*time.Millisecond)

	ctx := context.Background()

	// Start the scheduler
	s.Start(ctx)

	// Give it a moment to start
	time.Sleep(50 * time.Millisecond)

	// Test that Stop returns without hanging
	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Error("Stop did not return within 1 second")
	}

	// Test that calling Stop again does not panic and returns quickly
	done2 := make(chan struct{})
	go func() {
		s.Stop()
		close(done2)
	}()
	select {
	case <-done2:
	case <-time.After(1 * time.Second):
		t.Error("Second Stop did not return within 1 second")
	}
}

// TestSchedulerStopsRetryingPermanentlyFailingDelivery verifies that a reminder
// whose delivery fails every time is retried only MaxDeliveryAttempts times
// before being given up (marked dispatched), rather than looping forever every
// 30s against a permanently-failing destination.
func TestSchedulerStopsRetryingPermanentlyFailingDelivery(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	fl := &failSender{}
	dispatcher.RegisterSender("discord", fl)

	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })

	ctx := context.Background()
	if err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "julia",
		Gateway:    "discord",
		ExternalID: "bad-channel",
		Message:    "remind me anyway",
		DueAt:      clock.Add(-1 * time.Minute),
	}); err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	// Drive processDue repeatedly (as the 30s ticker would) until the reminder
	// is no longer due (it was given up), bailing out past a sane bound.
	var rounds int
	for {
		s.processDue(ctx)
		rounds++
		if rounds > MaxDeliveryAttempts+2 {
			t.Fatalf("reminder was not given up after %d rounds (sender calls=%d)", rounds, fl.calls)
		}
		due, _ := db.GetDueReminders(ctx, clock)
		if len(due) == 0 {
			break
		}
	}

	if rounds != MaxDeliveryAttempts {
		t.Errorf("expected %d processDue rounds before giving up, got %d", MaxDeliveryAttempts, rounds)
	}
	if fl.calls != MaxDeliveryAttempts {
		t.Errorf("expected sender called %d times, got %d", MaxDeliveryAttempts, fl.calls)
	}

	// Given-up reminders must not appear in the pending set.
	pending, _ := db.GetPendingReminders(ctx)
	if len(pending) != 0 {
		t.Errorf("expected 0 pending reminders after give-up, got %d", len(pending))
	}
}

// TestSchedulerGivesUpWhenIncrementDeliveryAttemptFails verifies that when
// IncrementDeliveryAttempt itself errors (e.g. a transient database fault),
// the scheduler still gives up — marks the reminder dispatched — rather than
// leaving it pending for infinite retries. The bounded-retry invariant must
// hold even when the attempt counter cannot be persisted.
func TestSchedulerGivesUpWhenIncrementDeliveryAttemptFails(t *testing.T) {
	db := newMockDB()
	db.incrementErr = errors.New("database locked")
	clock := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	fl := &failSender{}
	dispatcher.RegisterSender("discord", fl)

	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })

	ctx := context.Background()
	if err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "julia",
		Gateway:    "discord",
		ExternalID: "bad-channel",
		Message:    "remind me anyway",
		DueAt:      clock.Add(-1 * time.Minute),
	}); err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	// Drive processDue once. With the bug, IncrementDeliveryAttempt fails
	// and the function returns without marking dispatched, so the reminder
	// stays pending and would be retried forever. With the fix, the
	// reminder is given up (marked dispatched) on the first failure.
	s.processDue(ctx)

	// The reminder must have been dispatched (given up), not left pending.
	due, err := db.GetDueReminders(ctx, clock)
	if err != nil {
		t.Fatalf("GetDueReminders: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("expected 0 due reminders after processDue (reminder should be given up), got %d", len(due))
	}

	// No pending reminders — the bound was honoured.
	pending, err := db.GetPendingReminders(ctx)
	if err != nil {
		t.Fatalf("GetPendingReminders: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending reminders after give-up, got %d", len(pending))
	}
}
