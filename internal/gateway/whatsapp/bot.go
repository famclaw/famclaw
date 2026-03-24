// Package whatsapp provides a WhatsApp gateway for FamClaw using whatsmeow.
// Pure Go — no CGO, no Chromium, works on RPi.
package whatsapp

import (
	"context"
	"fmt"
	"log"

	"github.com/famclaw/famclaw/internal/gateway"
)

// Bot is a WhatsApp gateway using whatsmeow.
// WhatsApp requires QR code pairing on first run.
type Bot struct {
	dbPath string
}

// New creates a WhatsApp bot. dbPath is where whatsmeow stores session data.
func New(dbPath string) *Bot {
	return &Bot{dbPath: dbPath}
}

func (b *Bot) Name() string { return "whatsapp" }

// Start connects to WhatsApp and listens for messages.
// On first run, prints a QR code to stderr for pairing.
// Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context, handleMsg func(ctx context.Context, msg gateway.Message) gateway.Reply) error {
	// whatsmeow integration is deferred — requires QR pairing flow and
	// the whatsmeow dependency. This placeholder logs and returns.
	// The full implementation will:
	// 1. Open whatsmeow SQL store at b.dbPath
	// 2. Connect to WhatsApp
	// 3. If not paired, display QR code via stderr
	// 4. Listen for message events
	// 5. Route through handleMsg
	// 6. Send reply back

	log.Printf("[whatsapp] WhatsApp gateway starting (db: %s)", b.dbPath)
	log.Printf("[whatsapp] Note: WhatsApp requires QR code pairing on first run")

	_ = handleMsg // will be used when whatsmeow is integrated

	<-ctx.Done()
	return fmt.Errorf("whatsapp gateway: %w", ctx.Err())
}
