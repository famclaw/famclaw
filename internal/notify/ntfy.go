package notify

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// NtfyNotifier sends notifications via ntfy.sh (self-hosted push).
type NtfyNotifier struct {
	cfg config.NtfyConfig
}

// NewNtfyNotifier creates a new NtfyNotifier.
func NewNtfyNotifier(cfg config.NtfyConfig) *NtfyNotifier {
	return &NtfyNotifier{cfg: cfg}
}

func (n *NtfyNotifier) Notify(ctx context.Context, a *store.Approval, approveURL, denyURL string) error {
	title := fmt.Sprintf("%s needs approval for %q", a.UserDisplay, a.Category)
	body := fmt.Sprintf("Question: %s\nApprove: %s\nDeny: %s", a.QueryText, approveURL, denyURL)
	return n.post(ctx, title, body)
}

func (n *NtfyNotifier) NotifyDecision(ctx context.Context, a *store.Approval) error {
	title := fmt.Sprintf("Request %s", a.Status)
	body := formatDecisionMessage(a)
	return n.post(ctx, title, body)
}

func (n *NtfyNotifier) post(ctx context.Context, title, body string) error {
	ntfyURL := fmt.Sprintf("%s/%s", n.cfg.URL, n.cfg.Topic)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ntfyURL, bytes.NewBufferString(body))
	if err != nil {
		return fmt.Errorf("creating ntfy request: %w", err)
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", "high")
	req.Header.Set("Tags", "family,approval")
	if n.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.cfg.Token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("posting to ntfy: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned %d", resp.StatusCode)
	}
	return nil
}
