// Package gateway provides the messaging gateway abstraction for FamClaw.
// Each gateway (Telegram, WhatsApp, Discord) receives messages, routes them
// through identity → classifier → policy → agent, and sends replies.
package gateway

import "context"

// Message is an inbound message from any gateway.
type Message struct {
	Gateway     string // telegram | whatsapp | discord
	ExternalID  string // platform-specific user ID
	Text        string
	DisplayName string // from platform profile (best effort)
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
