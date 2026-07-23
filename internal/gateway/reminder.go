// Package gateway reminder scheduler.
//
// The ReminderScheduler polls the database for reminders whose due_at has
// passed and delivers them proactively through the user's linked gateway
// (Telegram, Discord) — without waiting for the user to send a message
// first. This is the proactive-delivery path. The on-next-message delivery
// in internal/agent.Agent.Chat remains as a fallback for gateways that do
// not implement gateway.Sender (e.g. web chat, WhatsApp placeholder).
package gateway

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/famclaw/famclaw/internal/store"
)

// ReminderScheduler polls the DB for due reminders and delivers them
// via the Router's SendTo. A Clock is injected so tests can drive
// due-firing deterministically without real sleeps.
type ReminderScheduler struct {
	db       *store.DB
	router   *Router
	clock    Clock
	interval time.Duration
}

// NewReminderScheduler creates a scheduler that polls for due reminders
// at the given interval and delivers them through the router.
func NewReminderScheduler(db *store.DB, router *Router, clock Clock, interval time.Duration) *ReminderScheduler {
	return &ReminderScheduler{
		db:       db,
		router:   router,
		clock:    clock,
		interval: interval,
	}
}

// Start launches the polling loop in a goroutine. The loop exits when ctx
// is cancelled (graceful shutdown).
func (s *ReminderScheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

func (s *ReminderScheduler) loop(ctx context.Context) {
	// Fire immediately on start so due reminders from before boot are
	// not delayed by a full interval.
	s.runOnce(ctx)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runOnce(ctx)
		}
	}
}

// runOnce performs a single poll for due reminders. Exposed for tests so
// they can call it directly with a fake clock — no real sleep needed.
func (s *ReminderScheduler) runOnce(ctx context.Context) {
	now := s.clock.Now().Unix()
	reminders, err := s.db.GetDueRemindersAllUsers(ctx, now)
	if err != nil {
		log.Printf("[reminder] error fetching due reminders: %v", err)
		return
	}
	for _, r := range reminders {
		if err := s.deliver(ctx, r); err != nil {
			log.Printf("[reminder] delivery error for reminder %d: %v", r.ID, err)
			// On delivery failure, keep the reminder in the DB so it
			// can be retried on the next poll. Delete only on success.
			continue
		}
		if err := s.db.DeleteReminder(r.ID); err != nil {
			log.Printf("[reminder] error deleting delivered reminder %d: %v", r.ID, err)
		}
	}
}

// deliver sends a single reminder via the router and saves it to
// conversation history so it appears in the web dashboard.
func (s *ReminderScheduler) deliver(ctx context.Context, r store.Reminder) error {
	msg := fmt.Sprintf("🔔 Reminder: %s", r.Message)

	// Save to conversation history (same conv ID scheme as the agent
	// and on-next-message fallback path).
	convID := conversationID(r.UserName)
	if err := s.db.SaveMessage(convID, r.UserName, "assistant", msg, "reminder", "allow"); err != nil {
		return fmt.Errorf("saving reminder to history: %w", err)
	}

	// Proactively deliver through the user's linked gateway.
	if err := s.router.SendTo(ctx, r.UserName, msg); err != nil {
		return fmt.Errorf("proactive send: %w", err)
	}

	log.Printf("[reminder] delivered reminder %d to %s", r.ID, r.UserName)
	return nil
}
