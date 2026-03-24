package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// DiscordNotifier sends notifications via Discord webhook.
type DiscordNotifier struct {
	webhookURL string
}

// NewDiscordNotifier creates a new DiscordNotifier.
func NewDiscordNotifier(cfg config.DiscordConfig) *DiscordNotifier {
	return &DiscordNotifier{webhookURL: cfg.WebhookURL}
}

func (d *DiscordNotifier) Notify(ctx context.Context, a *store.Approval, approveURL, denyURL string) error {
	return d.post(ctx, formatApprovalMessage(a, approveURL, denyURL))
}

func (d *DiscordNotifier) NotifyDecision(ctx context.Context, a *store.Approval) error {
	return d.post(ctx, formatDecisionMessage(a))
}

func (d *DiscordNotifier) post(ctx context.Context, content string) error {
	payload, _ := json.Marshal(map[string]string{"content": content})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating discord request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("posting to discord: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook returned %d", resp.StatusCode)
	}
	return nil
}
