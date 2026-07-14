// Package gateway provides the messaging gateway abstraction for FamClaw.
// Each gateway (Telegram, WhatsApp, Discord) receives messages, routes them
// through identity → classifier → policy → agent, and sends replies.
package gateway

import (
	"context"

	"github.com/famclaw/famclaw/internal/config"
)

// Message is an inbound message from any gateway.
type Message struct {
	Gateway     string // telegram | whatsapp | discord
	ExternalID  string // platform-specific user ID
	Text        string
	DisplayName string // from platform profile (best effort)
	GroupID     string // platform-specific group/channel ID (empty for direct messages)
	IsGroup     bool   // true if message is from a group/channel
	Attachments []Attachment // Attachments contains any attached media (images, etc.)
}
// Attachment represents an attached file or media.
type Attachment struct {
	// Type of attachment (e.g., "image")
	Type string
	// For images: base64-encoded data or URL
	// We'll use Data for base64-encoded content
	Data string
	// MIME type (e.g., "image/jpeg", "image/png")
	MIMEType string
}
type Reply struct {
	Text         string
	PolicyAction string // allow | block | request_approval | pending | onboarding
}

// MsgContext holds the gateway-specific context for a message.
// Used by tools that need to send outbound messages (e.g., reminders).
type MsgContext struct {
	Gateway     string // telegram | discord | whatsapp
	ExternalID  string // platform-specific user ID
	GroupID     string // platform-specific group/channel ID (empty for DMs)
	IsGroup     bool   // true if message is from a group/channel
}

// ChatFunc is the function signature for LLM chat.
// Matches the shape of agent.Chat but decoupled for testability.
type ChatFunc func(ctx context.Context, user *config.UserConfig, text string, msgCtx MsgContext) (string, error)

// Gateway is the interface that each messaging platform implements.
type Gateway interface {
	// Start begins listening for messages. Blocks until ctx is cancelled.
	// Calls handleMsg for each inbound message.
	Start(ctx context.Context, handleMsg func(ctx context.Context, msg Message) Reply) error
	// Name returns the gateway identifier (e.g. "telegram").
	Name() string
}

// Sender is an interface for sending outbound messages to a user/chat.
// Implemented by gateway bots to allow other components (reminders, etc.)
// to send messages without going through the inbound handler.
type Sender interface {
	// Send sends a message to the specified chat/user.
	// chatID is the gateway-specific identifier (e.g., Telegram chat_id, Discord channel_id).
	Send(ctx context.Context, chatID string, text string) error
}
