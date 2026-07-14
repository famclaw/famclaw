package reminder

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/store"
)

// ReminderStore is the interface for storing and retrieving reminders.
// Implemented by *store.DB and test mocks.
type ReminderStore interface {
	CreateReminder(ctx context.Context, r *store.Reminder) error
	GetDueReminders(ctx context.Context, now time.Time) ([]*store.Reminder, error)
	GetPendingReminders(ctx context.Context) ([]*store.Reminder, error)
	MarkReminderDispatched(ctx context.Context, id int64, now time.Time) error
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
	wg         sync.WaitGroup
	clock      func() time.Time // injectable for testing
}

// NewScheduler creates a new reminder scheduler.
// interval is how often to check for due reminders (default 30s if <=0).
func NewScheduler(db ReminderStore, dispatcher *Dispatcher, interval time.Duration) *Scheduler {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Scheduler{
		db:         db,
		dispatcher: dispatcher,
		interval:   interval,
		stopCh:     make(chan struct{}),
		clock:      time.Now,
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
	close(s.stopCh)
	s.mu.Unlock()
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

		if err := s.dispatcher.Dispatch(ctx, r); err != nil {
			log.Printf("[reminder] dispatch failed for reminder %d: %v", r.ID, err)
			continue
		}

		if err := s.db.MarkReminderDispatched(ctx, r.ID, now); err != nil {
			log.Printf("[reminder] mark dispatched failed for reminder %d: %v", r.ID, err)
		}
	}
}

// ReschedulePending loads all pending reminders and processes any that are already due.
// Call this on startup to handle reminders that were due while the service was down.
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
		if err := s.dispatcher.Dispatch(ctx, r); err != nil {
			log.Printf("[reminder] dispatch failed for reminder %d: %v", r.ID, err)
			continue
		}
		if err := s.db.MarkReminderDispatched(ctx, r.ID, now); err != nil {
			log.Printf("[reminder] mark dispatched failed for reminder %d: %v", r.ID, err)
		}
	}
}
