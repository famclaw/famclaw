package reminder

// Terminal-state regression tests (offline fault injection — mock senders,
// mock stores, no network). They pin down the invariant the scheduler broke
// before the terminal-state fix:
//
//   - a reminder that was NOT delivered to its target must never end up in
//     a delivered/dispatched-as-success terminal state;
//   - an absent or failed undelivered-record persistence combined with a
//     failed/absent fallback must not be closed out silently: the reminder
//     parks in the bounded 'undelivered_retry' state (persisted budget) and
//     terminalizes as 'undelivered' with record_lost evidence, never
//     claiming a dashboard record that does not exist;
//   - a successful delivery is idempotent within the process even when the
//     mark write faults.
//
// The corresponding pre-fix RED evidence (all six scenarios failing on a
// clean base) is captured in the task handoff for this change.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/store"
)

// countedUndelivered is a failing or healing undelivered store that counts
// its save attempts, so the bounded retry budget can be asserted exactly.
type countedUndelivered struct {
	mu    sync.Mutex
	recs  []*store.UndeliveredReminder
	fail  bool // save keeps failing
	saves int
}

func (c *countedUndelivered) SaveUndeliveredReminder(ctx context.Context, u *store.UndeliveredReminder) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saves++
	if c.fail {
		return errors.New("disk full")
	}
	c.recs = append(c.recs, u)
	return nil
}

func (c *countedUndelivered) savesAndRecords() (int, []*store.UndeliveredReminder) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*store.UndeliveredReminder, len(c.recs))
	copy(out, c.recs)
	return c.saves, out
}

// terminalStateReminder returns the single stored reminder under the mock
// db lock.
func terminalStateReminder(t *testing.T, db *mockDB) *store.Reminder {
	t.Helper()
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.reminders) != 1 {
		t.Fatalf("expected exactly 1 reminder in mock db, got %d", len(db.reminders))
	}
	return db.reminders[0]
}

// 1. Disabled sender: no sender is registered for the reminder's gateway.
// The bounded delivery budget exhausts; the healthy undelivered store
// records the content and the reminder must terminalize as 'undelivered'
// (truthful), never 'delivered'.
func TestTerminalStateDisabledSender(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher() // no sender registered for "discord"
	us := &countedUndelivered{}

	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })
	s.SetUndeliveredStore(us)

	ctx := context.Background()
	if err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "julia",
		Gateway:    "discord",
		ExternalID: "chan-1",
		Message:    "disabled sender",
		DueAt:      clock.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	for i := 0; i < MaxDeliveryAttempts; i++ {
		s.processDue(ctx)
	}

	r := terminalStateReminder(t, db)
	if r.DeliveryState != store.ReminderStateUndelivered {
		t.Errorf("delivery_state = %q, want %q (a never-delivered reminder must not be delivered/dispatched-as-success)", r.DeliveryState, store.ReminderStateUndelivered)
	}
	if r.DeliveryState == store.ReminderStateDelivered {
		t.Fatalf("false delivery: reminder marked delivered although no sender was registered")
	}
	if r.UndeliveredReason != ReasonRetriesExhausted {
		t.Errorf("undelivered_reason = %q, want %q", r.UndeliveredReason, ReasonRetriesExhausted)
	}
	saves, recs := us.savesAndRecords()
	if saves != 1 || len(recs) != 1 {
		t.Fatalf("record save attempts = %d, records = %d, want 1/1 (the healthy store records the content once)", saves, len(recs))
	}
	if recs[0].Message != "disabled sender" || recs[0].Reason != ReasonRetriesExhausted {
		t.Errorf("record = %+v, want the original content with reason %q", recs[0], ReasonRetriesExhausted)
	}
}

// 2. Storage failure: undelivered-record persistence keeps failing and no
// fallback is configured. The reminder must park in the bounded
// 'undelivered_retry' state after the first failure and terminalize as
// 'undelivered' with record_lost evidence exactly once the persisted budget
// (1 + MaxTerminalAttempts save attempts) is exhausted — never delivered.
func TestTerminalStateStorageFailureBoundedRetry(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	dispatcher.RegisterSender("discord", &errSender{err: deadChannelErr("dead-chan")})
	us := &countedUndelivered{fail: true}

	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })
	s.SetUndeliveredStore(us)

	ctx := context.Background()
	if err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "julia",
		Gateway:    "discord",
		ExternalID: "dead-chan",
		Message:    "storage failure",
		DueAt:      clock.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	s.processDue(ctx)
	r := terminalStateReminder(t, db)
	if r.DeliveryState != store.ReminderStateUndeliveredRetry {
		t.Fatalf("after round 1: delivery_state = %q, want %q (bounded retry, not terminal)", r.DeliveryState, store.ReminderStateUndeliveredRetry)
	}
	if r.TerminalAttempts != 1 {
		t.Fatalf("after round 1: terminal_attempts = %d, want 1", r.TerminalAttempts)
	}
	if r.UndeliveredReason != ReasonInvalidChat {
		t.Fatalf("undelivered_reason = %q, want %q (the original delivery reason must survive the retry)", r.UndeliveredReason, ReasonInvalidChat)
	}

	// Drive until the budget exhausts; bail well past it to catch
	// unbounded loops.
	for i := 0; i < MaxTerminalAttempts+2; i++ {
		s.processDue(ctx)
		if terminalStateReminder(t, db).DeliveryState == store.ReminderStateUndelivered {
			break
		}
	}

	r = terminalStateReminder(t, db)
	if r.DeliveryState != store.ReminderStateUndelivered {
		t.Fatalf("delivery_state = %q, want %q after the bounded budget exhausts", r.DeliveryState, store.ReminderStateUndelivered)
	}
	if r.DeliveryState == store.ReminderStateDelivered {
		t.Fatalf("false delivery: marked delivered although persistence kept failing")
	}
	if r.UndeliveredReason != ReasonInvalidChat {
		t.Errorf("undelivered_reason = %q, want %q", r.UndeliveredReason, ReasonInvalidChat)
	}
	saves, recs := us.savesAndRecords()
	if want := 1 + MaxTerminalAttempts; saves != want {
		t.Errorf("record save attempts = %d, want %d (1 initial + MaxTerminalAttempts bounded retries)", saves, want)
	}
	if len(recs) != 0 {
		t.Errorf("records = %d, want 0 — no dashboard record exists and none may be claimed", len(recs))
	}
}

// 3. Notification failure: the undelivered record saves fine but the
// fallback notification fails. The reminder still terminalizes as
// 'undelivered' (truthful — the target never received it), with the record
// present and no fallback message pushed.
func TestTerminalStateNotificationFailure(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	dispatcher.RegisterSender("discord", &errSender{err: deadChannelErr("dead-chan")})
	us := &countedUndelivered{}
	fb := &mockFallback{err: errors.New("telegram bot offline")}

	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })
	s.SetUndeliveredStore(us)
	s.SetFallback(fb)

	ctx := context.Background()
	if err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "julia",
		Gateway:    "discord",
		ExternalID: "dead-chan",
		Message:    "notification failure",
		DueAt:      clock.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	s.processDue(ctx)

	r := terminalStateReminder(t, db)
	if r.DeliveryState != store.ReminderStateUndelivered {
		t.Fatalf("delivery_state = %q, want %q", r.DeliveryState, store.ReminderStateUndelivered)
	}
	if r.UndeliveredReason != ReasonInvalidChat {
		t.Errorf("undelivered_reason = %q, want %q", r.UndeliveredReason, ReasonInvalidChat)
	}
	if _, recs := us.savesAndRecords(); len(recs) != 1 || recs[0].Message != "notification failure" {
		t.Errorf("record: the healthy store must keep the content, got %v", recs)
	}
	if sent := fb.getSent(); len(sent) != 0 {
		t.Errorf("fallback pushes = %d, want 0 (the notification failed)", len(sent))
	}
}

// 4. Restart/recovery: persistence fails in the first process (reminder
// parks in 'undelivered_retry') and heals before the second process starts.
// Recovery (ReschedulePending) must complete the terminalization with the
// original reason and recorded content — and must NOT re-attempt delivery
// (the user's target was already given up on).
func TestTerminalStateRestartRecovery(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	fl := &errSender{err: deadChannelErr("dead-chan")}
	dispatcher.RegisterSender("discord", fl)
	us := &countedUndelivered{}

	mkScheduler := func() *Scheduler {
		s := NewScheduler(db, dispatcher, 10*time.Millisecond)
		s.SetClock(func() time.Time { return clock })
		s.SetUndeliveredStore(us)
		return s
	}

	ctx := context.Background()
	if err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "julia",
		Gateway:    "discord",
		ExternalID: "dead-chan",
		Message:    "restart recovery",
		DueAt:      clock.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	// Process 1: persistence faults → bounded retry state.
	us.fail = true
	s1 := mkScheduler()
	s1.processDue(ctx)
	if r := terminalStateReminder(t, db); r.DeliveryState != store.ReminderStateUndeliveredRetry {
		t.Fatalf("process 1: delivery_state = %q, want %q", r.DeliveryState, store.ReminderStateUndeliveredRetry)
	}

	// Persistence heals; process 2 restarts over the same store.
	us.fail = false
	s2 := mkScheduler()
	s2.ReschedulePending(ctx)

	r := terminalStateReminder(t, db)
	if r.DeliveryState != store.ReminderStateUndelivered {
		t.Fatalf("after restart: delivery_state = %q, want %q (recovery must complete terminalization)", r.DeliveryState, store.ReminderStateUndelivered)
	}
	if r.UndeliveredReason != ReasonInvalidChat {
		t.Errorf("undelivered_reason = %q, want %q (original reason must survive the restart)", r.UndeliveredReason, ReasonInvalidChat)
	}
	saves, recs := us.savesAndRecords()
	if saves != 2 || len(recs) != 1 {
		t.Fatalf("save attempts = %d, records = %d, want 2/1 (one failed in process 1, one healed in process 2)", saves, len(recs))
	}
	if recs[0].Message != "restart recovery" {
		t.Errorf("record content = %q, want the original message", recs[0].Message)
	}
	if fl.count() != 1 {
		t.Errorf("sender calls = %d, want 1 — recovery retries terminalization, not delivery", fl.count())
	}
}

// 5. Double failure: transient delivery failures exhaust the delivery
// budget, and at give-up BOTH the undelivered-record persistence and the
// fallback notification fail. The reminder must not be closed out as
// delivered; it parks in the bounded retry state, and delivery must NOT be
// re-attempted on the retry ticks (only terminalization is retried).
func TestTerminalStateDoubleFailure(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	fl := &errSender{err: errors.New("502 bad gateway")}
	dispatcher.RegisterSender("discord", fl)
	us := &countedUndelivered{fail: true}
	fb := &mockFallback{err: errors.New("no alternate reachable gateway")}

	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })
	s.SetUndeliveredStore(us)
	s.SetFallback(fb)

	ctx := context.Background()
	if err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "julia",
		Gateway:    "discord",
		ExternalID: "chan-1",
		Message:    "double failure",
		DueAt:      clock.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	// Delivery budget exhausts (MaxDeliveryAttempts delivery attempts).
	for i := 0; i < MaxDeliveryAttempts; i++ {
		s.processDue(ctx)
	}
	r := terminalStateReminder(t, db)
	if fl.count() != MaxDeliveryAttempts {
		t.Fatalf("sender calls = %d, want %d (delivery budget unchanged)", fl.count(), MaxDeliveryAttempts)
	}
	if r.DeliveryState != store.ReminderStateUndeliveredRetry {
		t.Fatalf("delivery_state = %q, want %q (both persistence and fallback failed: bounded retry, not a false terminal)", r.DeliveryState, store.ReminderStateUndeliveredRetry)
	}

	// Retry ticks: terminalization is retried, delivery is not.
	for i := 0; i < MaxTerminalAttempts+2; i++ {
		s.processDue(ctx)
		if terminalStateReminder(t, db).DeliveryState == store.ReminderStateUndelivered {
			break
		}
	}

	r = terminalStateReminder(t, db)
	if r.DeliveryState != store.ReminderStateUndelivered {
		t.Fatalf("delivery_state = %q, want %q after the bounded budget", r.DeliveryState, store.ReminderStateUndelivered)
	}
	if r.UndeliveredReason != ReasonRetriesExhausted {
		t.Errorf("undelivered_reason = %q, want %q", r.UndeliveredReason, ReasonRetriesExhausted)
	}
	if fl.count() != MaxDeliveryAttempts {
		t.Errorf("sender calls = %d, want %d — undelivered_retry must not re-attempt delivery", fl.count(), MaxDeliveryAttempts)
	}
	saves, recs := us.savesAndRecords()
	if want := 1 + MaxTerminalAttempts; saves != want {
		t.Errorf("record save attempts = %d, want %d", saves, want)
	}
	if len(recs) != 0 {
		t.Errorf("records = %d, want 0 — no dashboard record exists and none may be claimed", len(recs))
	}
	if got := fb.attemptsCount(); got != 1 {
		t.Errorf("fallback attempts = %d, want 1 (best-effort notification attempted once at give-up, never re-attempted on retry ticks)", got)
	}
}

// 6. Idempotent successful delivery: the message goes out but the mark
// write faults. The success path must be idempotent within the process —
// no re-send on the next tick (the user already has it), and no false
// 'delivered' state is written.
func TestTerminalStateIdempotentSuccess(t *testing.T) {
	db := newMockDB()
	db.markErr = errors.New("database locked")
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	ms := &mockSender{}
	dispatcher.RegisterSender("telegram", ms)

	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })

	ctx := context.Background()
	if err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "julia",
		Gateway:    "telegram",
		ExternalID: "123",
		Message:    "idempotent success",
		DueAt:      clock.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	s.processDue(ctx)
	s.processDue(ctx)
	s.processDue(ctx)

	if got := len(ms.getSent()); got != 1 {
		t.Fatalf("reminder sent %d times although delivery succeeded once and the mark faulted; want 1 (in-process idempotency)", got)
	}
	if r := terminalStateReminder(t, db); r.DeliveryState == store.ReminderStateDelivered {
		t.Errorf("delivery_state = delivered although the mark write failed; the state must not claim delivery persistence")
	}
}

// 6b. Happy-path idempotency guard: delivery succeeds, the mark succeeds,
// and a second tick must not re-send or re-record anything.
func TestTerminalStateSuccessDeliveredOnce(t *testing.T) {
	db := newMockDB()
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	dispatcher := NewDispatcher()
	ms := &mockSender{}
	dispatcher.RegisterSender("telegram", ms)
	us := &countedUndelivered{}

	s := NewScheduler(db, dispatcher, 10*time.Millisecond)
	s.SetClock(func() time.Time { return clock })
	s.SetUndeliveredStore(us)

	ctx := context.Background()
	if err := db.CreateReminder(ctx, &store.Reminder{
		UserName:   "julia",
		Gateway:    "telegram",
		ExternalID: "123",
		Message:    "delivered once",
		DueAt:      clock.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("create reminder: %v", err)
	}

	s.processDue(ctx)
	s.processDue(ctx)

	if got := len(ms.getSent()); got != 1 {
		t.Fatalf("reminder sent %d times, want 1", got)
	}
	if r := terminalStateReminder(t, db); r.DeliveryState != store.ReminderStateDelivered {
		t.Errorf("delivery_state = %q, want %q", r.DeliveryState, store.ReminderStateDelivered)
	}
	if saves, recs := us.savesAndRecords(); saves != 0 || len(recs) != 0 {
		t.Errorf("undelivered saves = %d, records = %d, want 0/0 for a delivered reminder", saves, len(recs))
	}
}
