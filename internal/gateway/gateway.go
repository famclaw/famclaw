// Package gateway provides the messaging gateway abstraction for FamClaw.
// Each gateway (Telegram, Discord) receives messages, routes them
// through identity → classifier → policy → agent, and sends replies.
package gateway

import (
	"context"
	"errors"
	"fmt"

	"github.com/famclaw/famclaw/internal/config"
)

// Message is an inbound message from any gateway.
type Message struct {
	Gateway     string // telegram | discord
	ExternalID  string // platform-specific user ID
	Text        string
	DisplayName string       // from platform profile (best effort)
	GroupID     string       // platform-specific group/channel ID (empty for direct messages)
	IsGroup     bool         // true if message is from a group/channel
	Attachments []Attachment // Attachments contains any attached media (images, etc.)
}

// Attachment represents an attached file or media.
type Attachment struct {
	// Type of attachment: "image" or "audio". Images follow the
	// multimodal path (spliced into ContentParts below the policy gates);
	// audio is transcribed into text by the agent before the gates run.
	Type string
	// For images: base64-encoded data or URL
	// We'll use Data for base64-encoded content
	Data string
	// MIME type (e.g., "image/jpeg", "image/png", "audio/ogg")
	MIMEType string
}
type Reply struct {
	Text         string
	PolicyAction string // allow | block | request_approval | pending | onboarding
}

// MsgContext holds the gateway-specific context for a message.
// Used by tools that need to send outbound messages (e.g., reminders).
type MsgContext struct {
	Gateway    string // telegram | discord
	ExternalID string // platform-specific user ID
	GroupID    string // platform-specific group/channel ID (empty for DMs)
	IsGroup    bool   // true if message is from a group/channel
	// Attachments carries any media (images, audio) extracted by the
	// gateway adapter from the inbound platform message. Image
	// attachments are forwarded to the agent for the multimodal path;
	// audio attachments are transcribed into text before the policy
	// gates run.
	Attachments []Attachment
	// ConvID is the stable conversation ID computed by the router
	// using the idle-gap rule. When non-empty, the agent reuses it
	// instead of computing its own, ensuring router and agent share
	// the same conversation context.
	ConvID string
}

// ChatFunc is the function signature for LLM chat.
// Matches the shape of agent.Chat but decoupled for testability.
type ChatFunc func(ctx context.Context, user *config.UserConfig, text string, msgCtx MsgContext) (string, error)

// Transcriber converts raw audio bytes into a transcript string.
// The concrete implementation lives in the internal/transcribe package;
// the gateway depends only on this interface for testability and to
// avoid an import on the agent package.
// Implementations must not retain the audio data after returning.

type Transcriber interface {
	// TranscribeAudio transcribes the given audio data. mimeType is the
	// media type of the audio (e.g. "audio/ogg", "audio/mpeg") used as a
	// hint to the transcription service. Returns the transcript text
	// or an error if transcription fails.
	TranscribeAudio(ctx context.Context, audioData []byte, mimeType string) (string, error)
}

// VoiceUnavailableMsg is returned to the user when an audio attachment
// cannot be transcribed (transcriber unconfigured or errored). Never a
// silent drop — issue #310. The agent package references the same string
// via this exported constant so both code paths speak identically.
const VoiceUnavailableMsg = "voice isn't available. Please ask a grown-up to configure voice transcription, or try again later."

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

// ErrInvalidChat signals that the target chat/channel no longer exists on
// the platform — a 404 on send (deleted channel, removed bot, unknown user).
// Senders wrap it with InvalidChatError so callers can distinguish a
// permanently-invalid destination from a transient failure via
// errors.Is(err, ErrInvalidChat): retrying a 404 is pointless and only
// hammers the platform API (the reminder scheduler fast-paths it instead of
// burning its bounded retries).
var ErrInvalidChat = errors.New("chat no longer exists on the platform")

// InvalidChatError is the structured wrapper of ErrInvalidChat. Gateway is
// the gateway name ("telegram", "discord"), ChatID the identifier that 404'd
// (the value the caller passed to Send), and Cause the underlying platform
// API error.
type InvalidChatError struct {
	Gateway string
	ChatID  string
	Cause   error
}

// Error renders the invalid-chat condition with its context. The cause text
// comes from the platform API error body, which can carry credentials —
// callers logging it should redact first (see notify.RedactWebhookURLInError).
func (e *InvalidChatError) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("%s: %s chat %s: %v", ErrInvalidChat, e.Gateway, e.ChatID, "no detail")
	}
	return fmt.Sprintf("%s: %s chat %s: %v", ErrInvalidChat, e.Gateway, e.ChatID, e.Cause)
}

// Is reports identity with the ErrInvalidChat sentinel so
// errors.Is(err, gateway.ErrInvalidChat) matches this error directly.
func (e *InvalidChatError) Is(target error) bool {
	return target == ErrInvalidChat
}

// Unwrap exposes the underlying platform API error.
func (e *InvalidChatError) Unwrap() error { return e.Cause }

// OutboundFile is a file the agent wants to deliver to a user through a
// gateway. Name is the filename as presented to the platform (no path
// components); Data is the full file content.
type OutboundFile struct {
	Name string
	Data []byte
}

// OutboundDestination identifies where a delivery goes: the requester's
// current conversation. ExternalID is the platform user id; GroupID is the
// platform channel id when the conversation is a group (empty for DMs).
type OutboundDestination struct {
	ExternalID string
	GroupID    string
}

// FileSender is implemented by gateway bots that can deliver a file
// attachment to the requester's current conversation. caption is an
// optional short message shown alongside the file.
type FileSender interface {
	SendFile(ctx context.Context, dest OutboundDestination, file OutboundFile, caption string) error
}
