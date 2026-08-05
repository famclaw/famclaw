package reminder

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/store"
)

// MaxDeliveryAttempts is the number of consecutive failed proactive deliveries
// after which the scheduler gives up on a reminder, marking it dispatched so it
// is no longer retried. This prevents an indefinite retry loop against a
// permanently-failing destination (e.g. a Discord channel that 404s as
// "Unknown Channel" every 30s). The counter persists across restarts via the
// reminders.delivery_attempts column.
const MaxDeliveryAttempts = 3

// ReminderStore is the interface for storing and retrieving reminders.
// Implemented by *store.DB and test mocks.
type ReminderStore interface {
	CreateReminder(ctx context.Context, r *store.Reminder) error
	GetDueReminders(ctx context.Context, now time.Time) ([]*store.Reminder, error)
	GetPendingReminders(ctx context.Context) ([]*store.Reminder, error)
	MarkReminderDispatched(ctx context.Context, id int64, now time.Time) error
	IncrementDeliveryAttempt(ctx context.Context, id int64) (int, error)
}

// Dispatcher handles sending reminder messages through the appropriate gateway.
type Dispatcher struct {
	senders map[string]gateway.Sender
	mu      sync.RWMutex
}

// NewDispatcher creates a new reminder dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		senders: make(map[string]gateway.Sender),
	}
}

// RegisterSender registers a gateway sender.
func (d *Dispatcher) RegisterSender(name string, sender gateway.Sender) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.senders[name] = sender
}

// Dispatch sends a reminder message through the appropriate gateway.
func (d *Dispatcher) Dispatch(ctx context.Context, r *store.Reminder) error {
	d.mu.RLock()
	sender, ok := d.senders[r.Gateway]
	d.mu.RUnlock()

	if !ok {
		return nil // no sender for this gateway (e.g., WhatsApp placeholder)
	}

	// Use external_id as the chat ID for DMs, or group_id for groups
	chatID := r.ExternalID
	if r.IsGroup && r.GroupID != "" {
		chatID = r.GroupID
	}

	prefix := "⏰ Reminder: "
	message := prefix + r.Message

	return sender.Send(ctx, chatID, message)
}

// Scheduler manages the background reminder dispatch.
type Scheduler struct {
	db         ReminderStore
	dispatcher *Dispatcher
	interval   time.Duration
	mu         sync.Mutex
	running    bool
	stopCh     chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
	clock      func() time.Time // injectable for testing
	// failedReminders tracks reminders abandoned within the current process
	// lifetime because the database could not persist either the delivery
	// attempt counter or the dispatched flag. These are skipped on every
	// subsequent processDue/ReschedulePending iteration, guaranteeing that
	// no failure combination produces unbounded retries. The set is
	// process-volatile: on restart a reminder in this set gets one more
	// attempt (not a per-30s-forever storm).
	failedReminders map[int64]bool
}

// NewScheduler creates a new reminder scheduler.
// interval is how often to check for due reminders (default 30s if <=0).
func NewScheduler(db ReminderStore, dispatcher *Dispatcher, interval time.Duration) *Scheduler {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Scheduler{
		db:              db,
		dispatcher:      dispatcher,
		interval:        interval,
		stopCh:          make(chan struct{}),
		clock:           time.Now,
		failedReminders: make(map[int64]bool),
	}
}

// SetClock sets a custom clock function (for testing).
func (s *Scheduler) SetClock(clock func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = clock
}

// Start begins the scheduler loop.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	s.wg.Add(1)
	go s.run(ctx)
}

// Stop stops the scheduler and waits for the current iteration to complete.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
}

func (s *Scheduler) run(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Run once immediately on start
	s.processDue(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.processDue(ctx)
		}
	}
}

func (s *Scheduler) processDue(ctx context.Context) {
	now := s.clock()
	reminders, err := s.db.GetDueReminders(ctx, now)
	if err != nil {
		log.Printf("[reminder] error querying due reminders: %v", err)
		return
	}

	for _, r := range reminders {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		default:
		}

		s.dispatchOnce(ctx, r, now)
	}
}

// dispatchOnce delivers a single reminder. On success it marks the reminder
// dispatched. On failure it records a bounded delivery attempt and, once
// MaxDeliveryAttempts consecutive failures have accumulated, gives up (marks
// dispatched) so the reminder is never retried indefinitely against a
// permanently-failing destination.
//
// When the database cannot persist either the attempt counter or the
// dispatched flag, the reminder cannot be reliably tracked or marked. To
// guarantee that NO failure combination produces unbounded retries, the
// scheduler abandons the reminder in-memory (failedReminders) so it is
// skipped on every subsequent tick. This is process-volatile: a scheduler
// restart grants at most one more attempt, not a per-30s-forever storm.
func (s *Scheduler) dispatchOnce(ctx context.Context, r *store.Reminder, now time.Time) {
	// Skip reminders already abandoned in this process lifetime.
	s.mu.Lock()
	abandoned := s.failedReminders[r.ID]
	s.mu.Unlock()
	if abandoned {
		return
	}

	if err := s.dispatcher.Dispatch(ctx, r); err != nil {
		log.Printf("[reminder] dispatch failed for reminder %d: %v", r.ID, err)
		attempts, aerr := s.db.IncrementDeliveryAttempt(ctx, r.ID)
		if aerr != nil {
			log.Printf("[reminder] recording delivery failure for reminder %d: %v", r.ID, aerr)
			// The attempt counter could not be persisted (e.g. a transient
			// database fault). To guarantee the bounded-retry invariant —
			// no reminder retries without limit — give up now by marking the
			// reminder dispatched. A reminder that cannot record its own
			// failure is not being delivered reliably enough to justify
			// further retries.
			if merr := s.db.MarkReminderDispatched(ctx, r.ID, now); merr != nil {
				log.Printf("[reminder] give-up mark failed after attempt-counter fault for reminder %d: %v", r.ID, merr)
				s.mu.Lock()
				s.failedReminders[r.ID] = true
				s.mu.Unlock()
				log.Printf("[reminder] abandoned reminder %d in-memory (counter fault: %v, mark fault: %v) — no further retries this process", r.ID, aerr, merr)
			}
			return
		}
		if attempts >= MaxDeliveryAttempts {
			if merr := s.db.MarkReminderDispatched(ctx, r.ID, now); merr != nil {
				log.Printf("[reminder] give-up mark failed for reminder %d: %v", r.ID, merr)
				s.mu.Lock()
				s.failedReminders[r.ID] = true
				s.mu.Unlock()
				log.Printf("[reminder] abandoned reminder %d in-memory after %d failed delivery attempts (mark fault: %v) — no further retries this process", r.ID, attempts, merr)
			} else {
				log.Printf("[reminder] gave up on reminder %d after %d failed delivery attempts", r.ID, attempts)
			}
		}
		return
	}
	if err := s.db.MarkReminderDispatched(ctx, r.ID, now); err != nil {
		log.Printf("[reminder] mark dispatched failed for reminder %d: %v", r.ID, err)
	}
}

// ReschedulePending loads all pending reminders and processes any that are
// already due. Call this on startup to handle reminders that were due while the
// service was down.
func (s *Scheduler) ReschedulePending(ctx context.Context) {
	now := s.clock()
	reminders, err := s.db.GetPendingReminders(ctx)
	if err != nil {
		log.Printf("[reminder] error loading pending reminders: %v", err)
		return
	}

	for _, r := range reminders {
		if r.DueAt.After(now) {
			continue // not due yet
		}
		s.dispatchOnce(ctx, r, now)
	}
}
