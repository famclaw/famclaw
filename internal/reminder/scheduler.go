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
// after which the scheduler gives up on a reminder, moving it to the
// undelivered path so it is no longer retried. This prevents an indefinite
// retry loop against a permanently-failing destination (e.g. a Discord
// channel that 404s as "Unknown Channel" every 30s). The counter persists
// across restarts via the reminders.delivery_attempts column.
const MaxDeliveryAttempts = 3

// MaxTerminalAttempts is the bounded retry budget for terminalizing a
// reminder whose undelivered-record persistence failed: how many times the
// record save (and, while the row sits in 'undelivered_retry', its retry) is
// attempted before the reminder is closed out as 'undelivered' with
// record_lost evidence. The budget persists in reminders.terminal_attempts,
// so it is not reset by restarts.
const MaxTerminalAttempts = 3

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
	GetUndeliveredRetryReminders(ctx context.Context) ([]*store.Reminder, error)
	MarkReminderDispatched(ctx context.Context, id int64, now time.Time) error
	MarkUndelivered(ctx context.Context, id int64, now time.Time, reason string) error
	BeginUndeliveredRetry(ctx context.Context, id int64, reason string) error
	IncrementTerminalAttempt(ctx context.Context, id int64) error
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
		return fmt.Errorf("no sender registered for gateway %q", r.Gateway)
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
	// justParked holds the reminder ids that transitioned into
	// 'undelivered_retry' during the CURRENT processDue call. The
	// terminalization pass of that same tick skips them: a reminder just
	// parked gets its first record-save retry on the NEXT tick (or the next
	// process after a restart), which keeps the terminal budget's save
	// attempts spaced across ticks instead of doubled on the parking tick.
	// Per-tick only — never persisted.
	justParked map[int64]bool
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
		justParked:      make(map[int64]bool),
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

	// Terminalization pass: reminders sitting in the bounded
	// 'undelivered_retry' state get their undelivered-record persistence
	// retried (and terminalized once the budget is exhausted) — WITHOUT
	// re-attempting delivery. This is what makes the undelivered state
	// durable across ticks and restarts instead of one-shot. Reminders
	// parked earlier in this very tick are skipped so their retry budget is
	// consumed across ticks, not doubled here.
	s.processUndeliveredRetry(ctx, now)

	s.mu.Lock()
	s.justParked = make(map[int64]bool)
	s.mu.Unlock()
}

// processUndeliveredRetry retries terminalization for every reminder in the
// 'undelivered_retry' state (its undelivered-record persistence failed and it
// was parked in the bounded retry state by handleUndeliverable / a previous
// pass). Each call is one bounded step of the terminal budget: the record
// save is attempted once; on success the reminder terminalizes as
// 'undelivered' with the original reason; on failure it stays in the retry
// state until terminal_attempts reaches MaxTerminalAttempts, then it
// terminalizes as 'undelivered' with record_lost evidence. Delivery is never
// re-attempted here.
func (s *Scheduler) processUndeliveredRetry(ctx context.Context, now time.Time) {
	retrying, err := s.db.GetUndeliveredRetryReminders(ctx)
	if err != nil {
		log.Printf("[reminder] event=undelivered_retry_load reminder_load_failed=true detail=%v", redactErr(err))
		return
	}
	for _, r := range retrying {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		default:
		}

		s.mu.Lock()
		parked := s.justParked[r.ID]
		abandoned := s.failedReminders[r.ID]
		s.mu.Unlock()
		if parked {
			continue // first retry on the next tick (or next process)
		}
		if abandoned {
			// This process already abandoned the reminder after it could not
			// persist either the terminal budget or the truthful mark (see
			// abandonInMemory). Retrying the terminalization pass against it
			// would re-hammer a down store on every tick; the in-memory guard
			// keeps it out of the bounded retry for the rest of this process,
			// matching the failedReminders invariant documented above (a
			// restart gets one more attempt, not a per-tick storm). Without
			// this check the loop read only justParked and re-processed an
			// already-abandoned reminder indefinitely.
			continue
		}
		s.recordOrTerminalize(ctx, r, now, r.UndeliveredReason)
	}
}

// recordOrTerminalize is one step of the bounded terminalization retry:
// attempt to persist the undelivered record; on success terminalize with the
// original delivery-failure reason; on failure persist one consumed step of
// the terminal budget and stay in the retry state until
// terminal_attempts reaches MaxTerminalAttempts, then terminalize with
// record_lost evidence. No fallback re-notification here — the user was
// already notified (or not reachable) when the failure first happened, and
// re-notifying on every retry would spam. Delivery is never re-attempted.
func (s *Scheduler) recordOrTerminalize(ctx context.Context, r *store.Reminder, now time.Time, reason string) {
	if s.recordUndelivered(ctx, r, reason) {
		s.finalizeUndelivered(ctx, r, now, reason, true)
		return
	}
	if r.TerminalAttempts >= MaxTerminalAttempts {
		// Budget exhausted: close out truthfully. The content still lives in
		// the reminders row itself; no undelivered_reminders record exists,
		// and none is claimed.
		s.finalizeUndelivered(ctx, r, now, reason, false)
		return
	}
	// Consume one bounded retry step; persisting it (not just counting it in
	// memory) is what keeps the budget intact across restarts.
	if ierr := s.db.IncrementTerminalAttempt(ctx, r.ID); ierr != nil {
		// Budget accounting could not be persisted: the in-memory guard
		// bounds this process; a restart gets the documented one-more-attempt
		// (#395 invariant). Never silently close as delivered.
		log.Printf("[reminder] event=undelivered_retry reminder_id=%d increment_failed=true detail=%v", r.ID, redactErr(ierr))
		s.abandonInMemory(r, ierr)
		return
	}
	log.Printf("[reminder] event=undelivered_retry reminder_id=%d attempt=%d max=%d recorded=false detail=record persistence still failing; retrying on next tick",
		r.ID, r.TerminalAttempts+1, MaxTerminalAttempts)
}

// recordUndelivered persists the reminder content so a delivery failure is
// never silently lost. It reports whether the record was durably stored —
// callers must not claim a dashboard record exists when it returns false.
// If the store write itself fails, the content goes to the log as the last
// resort — it must never disappear.
func (s *Scheduler) recordUndelivered(ctx context.Context, r *store.Reminder, reason string) bool {
	if s.undelivered == nil {
		log.Printf("[reminder] event=undelivered_record reminder_id=%d recorded=false record_lost=true detail=no undelivered store configured; no dashboard record exists for this reminder", r.ID)
		return false
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
		log.Printf("[reminder] event=undelivered_record reminder_id=%d recorded=false record_lost=true reason=%s detail=%v",
			r.ID, reason, redactErr(err))
		return false
	}
	log.Printf("[reminder] event=undelivered_record reminder_id=%d recorded=true record_lost=false reason=%s", r.ID, reason)
	return true
}

// finalizeUndelivered terminalizes a reminder in the truthful 'undelivered'
// state (never 'delivered'/'dispatched-as-success') and writes the
// operational-evidence log line. recorded reports whether a
// undelivered_reminders row exists: when false, no dashboard record is
// claimed — the content survives only in the reminders row and this log line.
func (s *Scheduler) finalizeUndelivered(ctx context.Context, r *store.Reminder, now time.Time, reason string, recorded bool) {
	if merr := s.db.MarkUndelivered(ctx, r.ID, now, reason); merr != nil {
		// Even the truthful terminal state could not be persisted: the
		// in-memory abandonment guard bounds the process, and a restart
		// gets one more attempt (the documented process-volatile invariant
		// from #395). Never fall back to a false 'delivered' mark.
		log.Printf("[reminder] event=undelivered_terminal reminder_id=%d recorded=%v mark_failed=true detail=%v",
			r.ID, recorded, redactErr(merr))
		s.abandonInMemory(r, merr)
		return
	}
	if recorded {
		log.Printf("[reminder] event=undelivered_terminal reminder_id=%d reason=%s recorded=true record_lost=false detail=content in undelivered store; user notified status see preceding fallback log", r.ID, reason)
	} else {
		log.Printf("[reminder] event=undelivered_terminal reminder_id=%d reason=%s recorded=false record_lost=true detail=no undelivered store row exists; content preserved in reminders row only; no dashboard record claimed", r.ID, reason)
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
// The undeliverable path (handleUndeliverable) is truthful about terminal
// state: a reminder that could not be delivered is never marked delivered.
// When the database cannot persist either the give-up mark or the
// undelivered-retry transition, the reminder is abandoned in-memory
// (failedReminders) so no failure combination produces unbounded retries.
// The set is process-volatile: on restart a reminder in it gets one more
// attempt (not a per-30s-forever storm).
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
			// The message DID go out; only the persistence of the success
			// mark failed. Re-sending on the next tick would deliver the
			// reminder twice to a user who already received it, so the
			// success path is idempotent within the process: abandon in
			// memory (bounded), and let a restart — not the 30s tick — get
			// the one additional attempt the #395 process-volatile invariant
			// documents.
			log.Printf("[reminder] event=mark_delivered_failed reminder_id=%d detail=%v — user already received the reminder; no re-send this process", r.ID, redactErr(merr))
			s.abandonInMemory(r, merr)
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
			r.ID, r.Gateway, redactErr(err))
		s.handleUndeliverable(ctx, r, now, ReasonInvalidChat, err)
		return
	}

	// Transient failure: keep #332's bounded-retry semantics.
	log.Printf("[reminder] dispatch failed for reminder %d: %v", r.ID, redactErr(err))
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
// delivered to its target. Terminal state is truthful, never
// delivered/dispatched-as-success:
//
//   - the undelivered record is persisted (s.recordUndelivered reports
//     whether it actually was — an absent store or a failed save means NO
//     dashboard record exists, and none may be claimed),
//   - the user is notified through an alternate reachable channel when one
//     exists (best effort, at most once — the store/row, not the push, is
//     the record of what happened),
//   - when persistence is absent OR failed AND a durable terminal mark still
//     cannot be made, the reminder enters the BOUNDED 'undelivered_retry'
//     state (terminal_attempts budget, persisted, survives restarts) and is
//     retried by the terminalization pass instead of being silently closed,
//   - when the budget exhausts, the reminder terminalizes as 'undelivered'
//     with record_lost operational evidence (structured log + state on the
//     row; the content still lives in the reminders row itself).
func (s *Scheduler) handleUndeliverable(ctx context.Context, r *store.Reminder, now time.Time, reason string, cause error) {
	recorded := s.recordUndelivered(ctx, r, reason)
	s.notifyFallback(ctx, r, reason)

	if recorded {
		s.finalizeUndelivered(ctx, r, now, reason, true)
		return
	}
	// No durable record: no store configured, or the save failed.
	if s.undelivered == nil {
		// A store that does not exist cannot start existing on a later tick:
		// terminalize now with record_lost evidence (bounded, truthful).
		s.finalizeUndelivered(ctx, r, now, reason, false)
		return
	}
	if r.TerminalAttempts >= MaxTerminalAttempts {
		// The bounded retry budget is exhausted (this can only happen via the
		// terminalization pass re-entering after a restart — the pass
		// terminalizes at the budget itself, so in-process this is a safety
		// close-out).
		s.finalizeUndelivered(ctx, r, now, reason, false)
		return
	}
	// Transient persistence failure: park in the bounded undelivered_retry
	// state. terminal_attempts is bumped by the transition and persists, so
	// the budget is never reset by restarts. If even this transition fails
	// (the database is down), the in-memory abandonment guard keeps this
	// process bounded — exactly the #395 invariant.
	if terr := s.db.BeginUndeliveredRetry(ctx, r.ID, reason); terr != nil {
		log.Printf("[reminder] event=undelivered_retry_park reminder_id=%d recorded=false park_failed=true detail=%v", r.ID, redactErr(terr))
		s.abandonInMemory(r, terr)
		return
	}
	s.mu.Lock()
	s.justParked[r.ID] = true
	s.mu.Unlock()
	log.Printf("[reminder] event=undelivered_retry reminder_id=%d attempt=1 max=%d recorded=false detail=undelivered persistence failed; terminalization retried on later ticks/restarts", r.ID, MaxTerminalAttempts)
}

// notifyFallback delivers the undelivered-reminder notice to the user through
// an alternate reachable channel. It is best-effort and runs at most once per
// reminder: the undelivered record (or the reminders row itself) is the
// authoritative evidence — the push is a courtesy, and a failed (or absent)
// fallback only logs, without pretending a record or notification exists.
func (s *Scheduler) notifyFallback(ctx context.Context, r *store.Reminder, reason string) {
	if s.fallback == nil {
		log.Printf("[reminder] event=fallback_notify reminder_id=%d notified=false detail=no fallback channel configured", r.ID)
		return
	}
	gw, ferr := s.fallback.Deliver(ctx, r, undeliveredText(r, reason))
	if ferr != nil {
		log.Printf("[reminder] event=fallback_notify reminder_id=%d reason=%s notified=false detail=%v", r.ID, reason, redactErr(ferr))
		return
	}
	log.Printf("[reminder] event=fallback_notify reminder_id=%d reason=%s notified=true gateway=%s", r.ID, reason, gw)
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

// redactErr returns err's string with gateway bot/webhook credentials scrubbed
// for logging: platform API error bodies and request URLs have leaked bot
// tokens into plain log lines before.
func redactErr(err error) string {
	redacted := notify.RedactWebhookURLInError(err)
	if redacted == nil {
		return "<nil>"
	}
	return redacted.Error()
}

// ReschedulePending loads all pending reminders and processes any that are
// already due. Call this on startup to handle reminders that were due while
// the service was down. It also resumes the bounded terminalization retry
// for reminders parked in 'undelivered_retry' by a previous process — the
// persisted terminal_attempts budget keeps that recovery bounded.
func (s *Scheduler) ReschedulePending(ctx context.Context) {
	now := s.clock()
	reminders, err := s.db.GetPendingReminders(ctx)
	if err != nil {
		log.Printf("[reminder] error loading pending reminders: %v", err)
	} else {
		for _, r := range reminders {
			if r.DueAt.After(now) {
				continue // not due yet
			}
			s.dispatchOnce(ctx, r, now)
		}
	}

	s.processUndeliveredRetry(ctx, now)
}
