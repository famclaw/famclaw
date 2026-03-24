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

// SlackNotifier sends notifications via Slack webhook.
type SlackNotifier struct {
	webhookURL string
}

// NewSlackNotifier creates a new SlackNotifier.
func NewSlackNotifier(cfg config.SlackConfig) *SlackNotifier {
	return &SlackNotifier{webhookURL: cfg.WebhookURL}
}

func (s *SlackNotifier) Notify(ctx context.Context, a *store.Approval, approveURL, denyURL string) error {
	return s.post(ctx, formatApprovalMessage(a, approveURL, denyURL))
}

func (s *SlackNotifier) NotifyDecision(ctx context.Context, a *store.Approval) error {
	return s.post(ctx, formatDecisionMessage(a))
}

func (s *SlackNotifier) post(ctx context.Context, text string) error {
	payload, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("posting to slack: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack webhook returned %d", resp.StatusCode)
	}
	return nil
}
