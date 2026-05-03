package telegram

import (
	"context"
	"net/http"
	"time"

	"github.com/famclaw/famclaw/internal/gateway"
)

// NewWithEndpoint constructs a Bot pointed at a custom Bot API base URL.
// The standard New() targets https://api.telegram.org; this variant exists
// so integration tests can stand up an httptest.Server and exercise the
// real send path without leaving the process.
//
// Not for production use — the public Bot API is the only supported endpoint.
func NewWithEndpoint(token, endpoint string) *Bot {
	return &Bot{
		token:    token,
		endpoint: endpoint,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SendChunked sends text to chatID, splitting at Telegram's 4096-byte limit.
// Mirrors the chunking the Start() loop performs but is callable directly
// from tests so the polling loop does not need to be stood up.
func (b *Bot) SendChunked(ctx context.Context, chatID int64, text string) error {
	for _, chunk := range gateway.ChunkMessage(text, 4096) {
		if err := b.sendMessage(ctx, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}
