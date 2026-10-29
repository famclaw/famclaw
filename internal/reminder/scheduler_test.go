package reminder

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/gateway"
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
	markErr          error // if non-nil, MarkReminderDispatched returns this error
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
	if m.markErr != nil {
		return m.markErr
	}
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

	// R1 fix: with no sender registered for the gateway, Dispatch must surface
	// an error instead of returning nil. The scheduler treats nil as success and
	// would otherwise mark the reminder dispatched even though nothing was sent.
	err := d.Dispatch(ctx, r)
	if err == nil {
		t.Errorf("expected an error when no sender is registered, got nil")
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

// TestSchedulerAbandonsWhenBothIncrementAndMarkFail is the twin hole: when
// IncrementDeliveryAttempt fails AND the give-up MarkReminderDispatched also
// fails, the reminder is neither counted nor marked dispatched. Without an
// in-memory abandonment guard the scheduler retries on every tick, forever.
// This test asserts the scheduler abandons the reminder in-memory and never
// retries, even when both DB writes are unavailable.
func TestSchedulerAbandonsWhenBothIncrementAndMarkFail(t *testing.T) {
	db := newMockDB()
	db.incrementErr = errors.New("database locked")
	db.markErr = errors.New("database still locked")
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

	// Drive processDue multiple times (as the 30s ticker would). Without the
	// in-memory abandonment guard, each call re-dispatches because neither the
	// counter nor the mark could be persisted. With the fix, the scheduler
	// abandons the reminder after the first failure and never retries.
	for i := 0; i < 5; i++ {
		s.processDue(ctx)
	}

	if fl.calls != 1 {
		t.Errorf("expected sender called once then abandoned in-memory, got %d calls", fl.calls)
	}
}

// --- test doubles for the undeliverable-reminder path ---

// errSender is a gateway.Sender that fails every send with a fixed error and
// counts calls. Unlike failSender (a plain error = transient path) it can be
// pointed at gateway.ErrInvalidChat to exercise the 404 fast path.
type errSender struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (e *errSender) Send(ctx context.Context, chatID, text string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return e.err
}

func (e *errSender) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// mockFallback implements reminder.Fallback, recording what it would have
// pushed to the user on an alternate channel.
type mockFallback struct {
	mu   sync.Mutex
	sent []struct{ user, text string }
	gw   string
	err  error
}

func (f *mockFallback) Deliver(ctx context.Context, r *store.Reminder, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.sent = append(f.sent, struct{ user, text string }{r.UserName, text})
	if f.gw == "" {
		return "telegram", nil
	}
	return f.gw, nil
}

func (f *mockFallback) getSent() []struct {
	user string
	text string
} {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]struct {
		user string
		text string
	}, len(f.sent))
	copy(out, f.sent)
	return out
}

// mockUndelivered implements reminder.UndeliveredStore, recording every
// persisted undeliverable reminder.
type mockUndelivered struct {
	mu      sync.Mutex
	recs    []*store.UndeliveredReminder
	saveErr error
}

func (m *mockUndelivered) SaveUndeliveredReminder(ctx context.Context, u *store.UndeliveredReminder) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	m.recs = append(m.recs, u)
	return nil
}

func (m *mockUndelivered) get() []*store.UndeliveredReminder {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*store.UndeliveredReminder, len(m.recs))
	copy(out, m.recs)
	return out
}

// deadChannelErr builds the gateway error a Discord sender returns for a
// channel that no longer exists (the 404 storm's signature error).
func deadChannelErr(chatID string) error {
	return &gateway.InvalidChatError{
		Gateway: "discord",
		ChatID:  chatID,
		Cause:   errors.New("404: Unknown Channel (code: 0)"),
	}
}

// TestSchedulerInvalidChatFastPath is the B1 acceptance case: a reminder
// pointed at a 404 channel must not burn its retry budget (no infinite
// retry), the user must be notified via the fallback path with the
// reminder's content, and the target must be marked invalid.
func TestSchedulerInvalidChatFastPath(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	fl := &errSender{err: deadChannelErr("dead-chan")}
	dispatcher.RegisterSender("discord", fl)

	fb := &mockFallback{}
	us := &mockUndelivered{}

	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })
	s.SetFallback(fb)
	s.SetUndeliveredStore(us)

	ctx := context.Background()
	if err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "julia",
		Gateway:    "discord",
		ExternalID: "dead-chan",
		Message:    "remind me anyway",
		DueAt:      clock.Add(-1 * time.Minute),
	}); err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	s.processDue(ctx)

	if got := fl.count(); got != 1 {
		t.Errorf("sender calls = %d, want 1 (404 must fast-path, not retry)", got)
	}

	// The reminder is closed out (not silently dropped, not retried).
	if pending, _ := db.GetPendingReminders(ctx); len(pending) != 0 {
		t.Errorf("pending reminders = %d, want 0 after give-up", len(pending))
	}

	// The content is recorded for the user to query, with the cause.
	recs := us.get()
	if len(recs) != 1 {
		t.Fatalf("undelivered records = %d, want 1", len(recs))
	}
	if recs[0].Message != "remind me anyway" || recs[0].UserName != "julia" ||
		recs[0].Gateway != "discord" || recs[0].ChatID != "dead-chan" ||
		recs[0].Reason != ReasonInvalidChat {
		t.Errorf("record = %+v, want the original content with reason %q", recs[0], ReasonInvalidChat)
	}

	// The user is notified via the fallback path, with the content intact.
	sent := fb.getSent()
	if len(sent) != 1 {
		t.Fatalf("fallback notifications = %d, want 1", len(sent))
	}
	if sent[0].user != "julia" {
		t.Errorf("fallback recipient = %q, want julia", sent[0].user)
	}
	if !strings.Contains(sent[0].text, "remind me anyway") {
		t.Errorf("fallback notice must carry the reminder content, got: %q", sent[0].text)
	}
	if !strings.Contains(sent[0].text, "no longer exists") {
		t.Errorf("fallback notice must name the dead-channel condition, got: %q", sent[0].text)
	}
}

// TestSchedulerInvalidChatMarksTargetInvalid verifies the storm fix: when
// several reminders share one dead channel, the gateway API is called only
// once — the first 404 marks the target invalid and every later reminder to
// it skips the API call entirely (the 14k-line 404 storm was N reminders
// each hammering their own retry budget).
func TestSchedulerInvalidChatMarksTargetInvalid(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	fl := &errSender{err: deadChannelErr("dead-chan")}
	dispatcher.RegisterSender("discord", fl)

	fb := &mockFallback{}
	us := &mockUndelivered{}

	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })
	s.SetFallback(fb)
	s.SetUndeliveredStore(us)

	ctx := context.Background()
	for _, msg := range []string{"first reminder", "second reminder"} {
		if err := db.CreateReminder(ctx, &store.Reminder{
			UserName:   "julia",
			Gateway:    "discord",
			ExternalID: "dead-chan",
			Message:    msg,
			DueAt:      clock.Add(-1 * time.Minute),
		}); err != nil {
			t.Fatalf("create reminder: %v", err)
		}
	}

	s.processDue(ctx)

	if got := fl.count(); got != 1 {
		t.Errorf("sender calls = %d, want 1 (second reminder must not hit the API)", got)
	}
	if pending, _ := db.GetPendingReminders(ctx); len(pending) != 0 {
		t.Errorf("pending reminders = %d, want 0", len(pending))
	}
	recs := us.get()
	if len(recs) != 2 {
		t.Fatalf("undelivered records = %d, want 2 (each reminder's content is kept)", len(recs))
	}
	for i, want := range []string{"first reminder", "second reminder"} {
		if recs[i].Message != want || recs[i].Reason != ReasonInvalidChat {
			t.Errorf("record[%d] = %+v, want message %q reason %q", i, recs[i], want, ReasonInvalidChat)
		}
	}
}

// TestSchedulerTransient5xxBoundedRetry is the no-regression case: a
// transient 5xx must keep #332's bounded-retry semantics — exactly
// MaxDeliveryAttempts delivery attempts, then give-up — and the give-up must
// record the content and notify the user (not silently drop it).
func TestSchedulerTransient5xxBoundedRetry(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	fl := &errSender{err: errors.New("502 bad gateway")}
	dispatcher.RegisterSender("discord", fl)

	fb := &mockFallback{}
	us := &mockUndelivered{}

	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })
	s.SetFallback(fb)
	s.SetUndeliveredStore(us)

	ctx := context.Background()
	if err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "julia",
		Gateway:    "discord",
		ExternalID: "chan-1",
		Message:    "keep retrying me",
		DueAt:      clock.Add(-1 * time.Minute),
	}); err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	var rounds int
	for {
		s.processDue(ctx)
		rounds++
		if rounds > MaxDeliveryAttempts+2 {
			t.Fatalf("reminder was not given up after %d rounds (sender calls=%d)", rounds, fl.count())
		}
		if due, _ := db.GetDueReminders(ctx, clock); len(due) == 0 {
			break
		}
	}

	if got := fl.count(); got != MaxDeliveryAttempts {
		t.Errorf("sender calls = %d, want %d (bounded retry must be unchanged for transient errors)", got, MaxDeliveryAttempts)
	}
	if rounds != MaxDeliveryAttempts {
		t.Errorf("processDue rounds = %d, want %d", rounds, MaxDeliveryAttempts)
	}

	recs := us.get()
	if len(recs) != 1 {
		t.Fatalf("undelivered records = %d, want 1", len(recs))
	}
	if recs[0].Reason != ReasonRetriesExhausted || recs[0].Message != "keep retrying me" {
		t.Errorf("record = %+v, want reason %q with the content", recs[0], ReasonRetriesExhausted)
	}
	sent := fb.getSent()
	if len(sent) != 1 || !strings.Contains(sent[0].text, "keep retrying me") {
		t.Errorf("fallback notices = %d with content, want 1 carrying the reminder text", len(sent))
	}
	if pending, _ := db.GetPendingReminders(ctx); len(pending) != 0 {
		t.Errorf("pending reminders = %d, want 0", len(pending))
	}
}

// TestSchedulerInvalidChatNoFallbackStillRecorded covers the "else" branch:
// with no alternate channel configured the notice cannot be pushed — but
// the content must still be recorded (never silently lost) and the reminder
// closed out.
func TestSchedulerInvalidChatNoFallbackStillRecorded(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	fl := &errSender{err: deadChannelErr("dead-chan")}
	dispatcher.RegisterSender("discord", fl)

	us := &mockUndelivered{}
	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })
	s.SetUndeliveredStore(us) // no SetFallback

	ctx := context.Background()
	if err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "julia",
		Gateway:    "discord",
		ExternalID: "dead-chan",
		Message:    "silent-loss check",
		DueAt:      clock.Add(-1 * time.Minute),
	}); err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	s.processDue(ctx)

	if got := fl.count(); got != 1 {
		t.Errorf("sender calls = %d, want 1", got)
	}
	recs := us.get()
	if len(recs) != 1 || recs[0].Message != "silent-loss check" {
		t.Fatalf("content must be recorded even without a fallback channel, got: %+v", recs)
	}
	if pending, _ := db.GetPendingReminders(ctx); len(pending) != 0 {
		t.Errorf("pending reminders = %d, want 0", len(pending))
	}
}

// TestSchedulerInvalidChatStoreWriteFails covers the last-resort branch: if
// the undelivered-store write itself fails, the reminder must still be
// closed out (no retry loop, no panic) — the content goes to the log
// instead.
func TestSchedulerInvalidChatStoreWriteFails(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	fl := &errSender{err: deadChannelErr("dead-chan")}
	dispatcher.RegisterSender("discord", fl)

	us := &mockUndelivered{saveErr: errors.New("disk full")}
	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })
	s.SetUndeliveredStore(us)

	ctx := context.Background()
	if err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "julia",
		Gateway:    "discord",
		ExternalID: "dead-chan",
		Message:    "store down",
		DueAt:      clock.Add(-1 * time.Minute),
	}); err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	s.processDue(ctx)
	s.processDue(ctx) // must not retry: target is marked invalid

	if got := fl.count(); got != 1 {
		t.Errorf("sender calls = %d, want 1", got)
	}
	if pending, _ := db.GetPendingReminders(ctx); len(pending) != 0 {
		t.Errorf("pending reminders = %d, want 0 (give-up must hold even when the store write fails)", len(pending))
	}
}

// TestUndeliveredText verifies the user-facing notice: it always carries the
// original reminder content and the user, says what to do for a dead
// channel, and never leaks the raw platform error (which can carry
// credentials).
func TestUndeliveredText(t *testing.T) {
	r := &store.Reminder{
		ID:         4,
		UserName:   "julia",
		Gateway:    "discord",
		ExternalID: "dead-chan",
		Message:    "take out the trash",
	}
	cases := []struct {
		name    string
		reason  string
		wantSub []string
	}{
		{
			name:    "invalid chat names the dead-channel condition",
			reason:  ReasonInvalidChat,
			wantSub: []string{"take out the trash", "julia", "no longer exists", "re-point"},
		},
		{
			name:    "retries exhausted names the retry budget",
			reason:  ReasonRetriesExhausted,
			wantSub: []string{"take out the trash", "julia", "attempts"},
		},
		{
			name:    "counter fault explains the early give-up",
			reason:  ReasonCounterFault,
			wantSub: []string{"take out the trash", "julia", "counter"},
		},
		{
			name:    "unknown reason still carries the content",
			reason:  "mystery",
			wantSub: []string{"take out the trash", "julia"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := undeliveredText(r, tc.reason)
			for _, sub := range tc.wantSub {
				if !strings.Contains(text, sub) {
					t.Errorf("notice %q should contain %q", text, sub)
				}
			}
			if strings.Contains(text, "Unknown Channel") || strings.Contains(text, "404:") {
				t.Errorf("notice must not leak the raw platform error: %q", text)
			}
		})
	}
}
