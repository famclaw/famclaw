package reminder

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/store"
)

// MaxDeliveryAttempts is the number of consecutive failed proactive deliveries
// after which the scheduler gives up on a reminder, marking it dispatched so
// it is no longer retried. This prevents an indefinite retry loop against a
// permanently-failing destination (e.g. a Discord channel that 404s as
// "Unknown Channel" every 30s). The counter persists across restarts via the
// reminders.delivery_attempts column.
const MaxDeliveryAttempts = 3

// Failure reasons recorded in the undelivered-reminder store (and in logs).
// They are machine-readable identifiers, not user-facing prose — the user
// notice is built by undeliveredText.
const (
	// ReasonInvalidChat: the target chat no longer exists (a 404 on send:
	// deleted channel, removed bot, unknown user). A permanent failure that
	// fast-paths past the bounded retry.
	ReasonInvalidChat = "invalid_chat"
	// ReasonRetriesExhausted: a transient failure repeated until
	// MaxDeliveryAttempts was reached.
	ReasonRetriesExhausted = "retries_exhausted"
	// ReasonCounterFault: the delivery-attempt counter could not be
	// persisted, so the reminder was given up immediately to keep the
	// bounded-retry invariant (see dispatchOnce).
	ReasonCounterFault = "attempt_counter_fault"
)

// ReminderStore is the interface for storing and retrieving reminders.
// Implemented by *store.DB and test mocks.
type ReminderStore interface {
	CreateReminder(ctx context.Context, r *store.Reminder) error
	GetDueReminders(ctx context.Context, now time.Time) ([]*store.Reminder, error)
	GetPendingReminders(ctx context.Context) ([]*store.Reminder, error)
	MarkReminderDispatched(ctx context.Context, id int64, now time.Time) error
	IncrementDeliveryAttempt(ctx context.Context, id int64) (int, error)
}

// UndeliveredStore persists reminders that could not be delivered, so their
// content is never silently lost and the user can query what they missed
// (web: /api/reminders/undelivered). Implemented by *store.DB and test mocks.
type UndeliveredStore interface {
	SaveUndeliveredReminder(ctx context.Context, u *store.UndeliveredReminder) error
}

// Fallback delivers an undelivered-reminder notice to the user through an
// alternate reachable gateway — e.g. a reminder whose Discord channel is dead
// goes out over Telegram when the user is reachable there. Implementations
// must honor the platform initiation rules (a bot cannot start a Telegram
// conversation the user never opened) and must skip the target the reminder
// just failed on when another option exists. Deliver returns the gateway used
// and an error when the user cannot be reached on any other channel — in
// that case the undelivered-reminder store carries the content, so nothing
// is silently lost.
type Fallback interface {
	Deliver(ctx context.Context, r *store.Reminder, text string) (string, error)
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

// targetKey is the delivery target a reminder is pointed at: the gateway
// name plus the exact chat id the dispatcher hands to Send (external_id for
// DMs, group_id for groups). It keys the invalidTargets set so a dead
// channel is recognized by the same value every reminder to it uses.
func targetKey(r *store.Reminder) string {
	chatID := r.ExternalID
	if r.IsGroup && r.GroupID != "" {
		chatID = r.GroupID
	}
	return r.Gateway + "|" + chatID
}

// Scheduler manages the background reminder dispatch.
type Scheduler struct {
	db          ReminderStore
	dispatcher  *Dispatcher
	interval    time.Duration
	mu          sync.Mutex
	running     bool
	stopCh      chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
	clock       func() time.Time // injectable for testing
	fallback    Fallback
	undelivered UndeliveredStore
	// failedReminders tracks reminders abandoned within the current process
	// lifetime because the database could not persist either the delivery
	// attempt counter or the dispatched flag. These are skipped on every
	// subsequent processDue/ReschedulePending iteration, guaranteeing that
	// no failure combination produces unbounded retries. The set is
	// process-volatile: on restart a reminder in this set gets one more
	// attempt (not a per-30s-forever storm).
	failedReminders map[int64]bool
	// invalidTargets tracks delivery targets (gateway|chatID, see
	// targetKey) observed to be dead — a 404 on send — within the current
	// process lifetime. A reminder pointed at a known-dead target skips the
	// gateway API call entirely and goes straight to the undelivered path:
	// this is what stops the 404 storm when several reminders share one dead
	// channel (each one used to burn its own retry budget hammering the API).
	// Process-volatile like failedReminders: after a restart a target gets
	// one more attempt, which also lets a re-created channel self-heal.
	invalidTargets map[string]bool
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
		invalidTargets:  make(map[string]bool),
	}
}

// SetClock sets a custom clock function (for testing).
func (s *Scheduler) SetClock(clock func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = clock
}

// SetFallback installs the alternate-channel notifier used when a reminder
// cannot be delivered. Without one, undeliverable reminders are recorded in
// the undelivered store (if set) and logged — the content is still never
// silently lost, just not pushed to the user.
func (s *Scheduler) SetFallback(f Fallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallback = f
}

// SetUndeliveredStore installs the store that persists undeliverable
// reminder content. Without one, the content is logged instead.
func (s *Scheduler) SetUndeliveredStore(us UndeliveredStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.undelivered = us
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
// dispatched.
//
// Failure handling has two paths:
//
//   - INVALID CHAT (a 404 on send, gateway.ErrInvalidChat): the target is
//     gone, so retrying is pointless. The target is marked invalid for the
//     rest of the process (every subsequent reminder to it skips the API
//     call — this is what stops a 404 storm), and the reminder is handled
//     as undeliverable immediately: content recorded, user notified, marked
//     dispatched. No retry budget is consumed.
//   - TRANSIENT failure: #332's bounded retry applies — up to
//     MaxDeliveryAttempts consecutive failures, then give up. Giving up
//     never means silent loss: the content is recorded in the undelivered
//     store and the user is notified through an alternate channel.
//
// When the database cannot persist the give-up mark, the reminder is
// abandoned in-memory (failedReminders) so no failure combination produces
// unbounded retries. The set is process-volatile: on restart a reminder in
// it gets one more attempt (not a per-30s-forever storm).
func (s *Scheduler) dispatchOnce(ctx context.Context, r *store.Reminder, now time.Time) {
	// Skip reminders already abandoned in this process lifetime, and take
	// the fast path for a target already known to be dead.
	s.mu.Lock()
	abandoned := s.failedReminders[r.ID]
	knownInvalid := s.invalidTargets[targetKey(r)]
	s.mu.Unlock()
	if abandoned {
		return
	}

	if knownInvalid {
		// Dead target: do NOT hit the gateway API again — that is what
		// caused the 14k-line 404 storm. The reminder is recorded (content
		// never lost) and the user is notified, without another API call.
		s.handleUndeliverable(ctx, r, now, ReasonInvalidChat, nil)
		return
	}

	err := s.dispatcher.Dispatch(ctx, r)
	if err == nil {
		if merr := s.db.MarkReminderDispatched(ctx, r.ID, now); merr != nil {
			log.Printf("[reminder] mark dispatched failed for reminder %d: %v", r.ID, merr)
		}
		return
	}

	if errors.Is(err, gateway.ErrInvalidChat) {
		// 404 / dead chat: mark the target invalid so every reminder
		// pointed at it skips the API call from now on, then deliver the
		// content through the undelivered path (never silently lost).
		s.mu.Lock()
		s.invalidTargets[targetKey(r)] = true
		s.mu.Unlock()
		log.Printf("[reminder] reminder %d: %s chat no longer exists; target marked invalid: %v",
			r.ID, r.Gateway, redact(err))
		s.handleUndeliverable(ctx, r, now, ReasonInvalidChat, err)
		return
	}

	// Transient failure: keep #332's bounded-retry semantics.
	log.Printf("[reminder] dispatch failed for reminder %d: %v", r.ID, redact(err))
	attempts, aerr := s.db.IncrementDeliveryAttempt(ctx, r.ID)
	if aerr != nil {
		log.Printf("[reminder] recording delivery failure for reminder %d: %v", r.ID, aerr)
		// The attempt counter could not be persisted (e.g. a transient
		// database fault). To guarantee the bounded-retry invariant — no
		// reminder retries without limit — give up now: a reminder that
		// cannot record its own failure is not being delivered reliably
		// enough to justify further retries.
		s.handleUndeliverable(ctx, r, now, ReasonCounterFault, err)
		return
	}
	if attempts >= MaxDeliveryAttempts {
		log.Printf("[reminder] gave up on reminder %d after %d failed delivery attempts", r.ID, attempts)
		s.handleUndeliverable(ctx, r, now, ReasonRetriesExhausted, err)
	}
}

// handleUndeliverable is the end of the line for a reminder that will not be
// delivered: the content is persisted to the undelivered-reminder store (so
// it is never silently lost and the user can query it), the user is notified
// through an alternate reachable channel when one exists (best effort — the
// store is the authoritative record), and the reminder is marked dispatched
// so the retry loop stops.
func (s *Scheduler) handleUndeliverable(ctx context.Context, r *store.Reminder, now time.Time, reason string, cause error) {
	s.recordUndelivered(ctx, r, reason)
	notified := s.notifyFallback(ctx, r, reason)
	if merr := s.db.MarkReminderDispatched(ctx, r.ID, now); merr != nil {
		s.abandonInMemory(r, merr)
		return
	}
	if notified {
		log.Printf("[reminder] reminder %d undeliverable (%s): content recorded, user notified via fallback, marked dispatched", r.ID, reason)
	} else {
		log.Printf("[reminder] reminder %d undeliverable (%s): content recorded, no reachable fallback channel, marked dispatched", r.ID, reason)
	}
}

// recordUndelivered persists the reminder content so a delivery failure is
// never silently lost. If the store write itself fails, the content goes to
// the log as the last resort — it must never disappear.
func (s *Scheduler) recordUndelivered(ctx context.Context, r *store.Reminder, reason string) {
	if s.undelivered == nil {
		return
	}
	chatID := r.ExternalID
	if r.IsGroup && r.GroupID != "" {
		chatID = r.GroupID
	}
	u := &store.UndeliveredReminder{
		ReminderID: r.ID,
		UserName:   r.UserName,
		Gateway:    r.Gateway,
		ChatID:     chatID,
		Message:    r.Message,
		Reason:     reason,
	}
	if err := s.undelivered.SaveUndeliveredReminder(ctx, u); err != nil {
		log.Printf("[reminder] storing undelivered reminder %d failed: %v — content: %q", r.ID, err, r.Message)
	}
}

// notifyFallback delivers the undelivered-reminder notice to the user
// through an alternate reachable channel. It returns true when the notice
// was delivered. A failed (or absent) fallback only logs: the undelivered
// store is the authoritative record, so the content is never lost — but the
// primary goal is that the user actually sees it.
func (s *Scheduler) notifyFallback(ctx context.Context, r *store.Reminder, reason string) bool {
	if s.fallback == nil {
		log.Printf("[reminder] reminder %d (%s): no fallback channel configured; content recorded in undelivered store only", r.ID, reason)
		return false
	}
	gw, ferr := s.fallback.Deliver(ctx, r, undeliveredText(r, reason))
	if ferr != nil {
		log.Printf("[reminder] fallback notification for reminder %d (%s) failed: %v — content recorded in undelivered store", r.ID, reason, redact(ferr))
		return false
	}
	log.Printf("[reminder] delivered undelivered-reminder notice for %d to %s via %s", r.ID, r.UserName, gw)
	return true
}

// undeliveredText builds the user-facing notice. The original reminder
// content is always included so the user is never left in the dark. The raw
// gateway error is deliberately NOT included: platform API error bodies can
// carry credentials (a bot token leaked into plain log lines in the 404
// storm that motivated this work).
func undeliveredText(r *store.Reminder, reason string) string {
	var why string
	switch reason {
	case ReasonInvalidChat:
		why = fmt.Sprintf("your reminder is pointed at a %s chat that no longer exists (deleted channel or removed bot). Update or re-point where this reminder should go.", r.Gateway)
	case ReasonRetriesExhausted:
		why = fmt.Sprintf("delivery to %s kept failing and %d delivery attempts were used up.", r.Gateway, MaxDeliveryAttempts)
	case ReasonCounterFault:
		why = "the delivery-failure counter could not be persisted, so the reminder was removed from the retry queue."
	default:
		why = "delivery failed."
	}
	return fmt.Sprintf("⚠️ Reminder not delivered: %s Reminder for %s: %s", why, r.UserName, r.Message)
}

// abandonInMemory gives up on a reminder that could not be marked dispatched
// (e.g. the database is down): the in-memory set guarantees no further
// retries this process. The content was already recorded (or logged) by
// recordUndelivered before this is reached.
func (s *Scheduler) abandonInMemory(r *store.Reminder, merr error) {
	s.mu.Lock()
	s.failedReminders[r.ID] = true
	s.mu.Unlock()
	log.Printf("[reminder] abandoned reminder %d in-memory (mark fault: %v) — no further retries this process", r.ID, merr)
}

// redact returns err's string with gateway bot/webhook credentials scrubbed
// for logging: platform API error bodies and request URLs have leaked bot
// tokens into plain log lines before.
func redact(err error) string {
	redacted := notify.RedactWebhookURLInError(err)
	if redacted == nil {
		return "<nil>"
	}
	return redacted.Error()
}

// ReschedulePending loads all pending reminders and processes any that are
// already due. Call this on startup to handle reminders that were due while
// the service was down.
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
