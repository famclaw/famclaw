package notify

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// SMSNotifier sends notifications via Twilio SMS.
type SMSNotifier struct {
	cfg config.SMSConfig
}

// NewSMSNotifier creates a new SMSNotifier.
func NewSMSNotifier(cfg config.SMSConfig) *SMSNotifier {
	return &SMSNotifier{cfg: cfg}
}

func (s *SMSNotifier) Notify(ctx context.Context, a *store.Approval, approveURL, denyURL string) error {
	body := fmt.Sprintf("FamClaw: %s wants approval for %q.\nApprove: %s\nDeny: %s",
		a.UserDisplay, a.Category, approveURL, denyURL)
	return s.send(ctx, body)
}

func (s *SMSNotifier) NotifyDecision(ctx context.Context, a *store.Approval) error {
	body := fmt.Sprintf("FamClaw: %s's request for %q was %s.",
		a.UserDisplay, a.Category, a.Status)
	return s.send(ctx, body)
}

func (s *SMSNotifier) send(ctx context.Context, body string) error {
	for _, to := range s.cfg.ToNumbers {
		twilioURL := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Messages.json", s.cfg.AccountSID)

		data := url.Values{}
		data.Set("From", s.cfg.FromNumber)
		data.Set("To", to)
		data.Set("Body", body)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, twilioURL, strings.NewReader(data.Encode()))
		if err != nil {
			return fmt.Errorf("creating twilio request: %w", err)
		}
		req.SetBasicAuth(s.cfg.AccountSID, s.cfg.AuthToken)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("sending sms to %s: %w", to, err)
		}
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("twilio returned %d for %s", resp.StatusCode, to)
		}
	}
	return nil
}
