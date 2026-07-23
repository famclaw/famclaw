// Package gateway provides the messaging gateway abstraction for FamClaw.
// Each gateway (Telegram, WhatsApp, Discord) receives messages, routes them
// through identity → classifier → policy → agent, and sends replies.
package gateway

import (
	"context"
	"time"
)

// Message is an inbound message from any gateway.
type Message struct {
	Gateway     string // telegram | whatsapp | discord
	ExternalID  string // platform-specific user ID
	Text        string
	DisplayName string // from platform profile (best effort)
	GroupID     string // platform-specific group/channel ID (empty for direct messages)
	IsGroup     bool   // true if message is from a group/channel
}

// Reply is an outbound response to send back through the gateway.
type Reply struct {
	Text         string
	PolicyAction string // allow | block | request_approval | pending | onboarding
}

// Gateway is the interface that each messaging platform implements.
type Gateway interface {
	// Start begins listening for messages. Blocks until ctx is cancelled.
	// Calls handleMsg for each inbound message.
	Start(ctx context.Context, handleMsg func(ctx context.Context, msg Message) Reply) error
	// Name returns the gateway identifier (e.g. "telegram").
	Name() string
}

// Sender is an optional capability that gateways may implement to support
// bot-initiated (outbound) messages at a scheduled time — used by the
// reminder scheduler to deliver reminders proactively without waiting for
// the user to send a message first.
//
// externalID is the platform-specific user identifier (the same value that
// gateway.Message.ExternalID carries on inbound messages). For Telegram
// direct messages this is the chat_id; for Discord it is the user's snowflake
// ID (the gateway creates a DM channel internally).
type Sender interface {
	SendMessage(ctx context.Context, externalID, text string) error
}

// Clock is the time abstraction used by the reminder scheduler so tests
// can inject a fake clock and drive due-firing deterministically without
// real sleeps.
type Clock interface {
	Now() time.Time
}

// realClock is the production Clock backed by time.Now().
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// RealClock returns a Clock backed by the system clock.
func RealClock() Clock { return realClock{} }
