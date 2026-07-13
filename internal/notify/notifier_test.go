package notify

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

type mockNotifier struct {
	notifyCalled   atomic.Int32
	decisionCalled atomic.Int32
	shouldErr      bool
}

func (m *mockNotifier) Notify(ctx context.Context, a *store.Approval, approveURL, denyURL string) error {
	m.notifyCalled.Add(1)
	if m.shouldErr {
		return fmt.Errorf("mock error")
	}
	return nil
}

func (m *mockNotifier) NotifyDecision(ctx context.Context, a *store.Approval) error {
	m.decisionCalled.Add(1)
	if m.shouldErr {
		return fmt.Errorf("mock error")
	}
	return nil
}

var testApproval = &store.Approval{
	ID:          "test-123",
	UserName:    "alice",
	UserDisplay: "Alice",
	AgeGroup:    "age_8_12",
	Category:    "social_media",
	QueryText:   "Can I make an Instagram account?",
	Status:      "pending",
}

func TestGenerateToken(t *testing.T) {
	token := GenerateToken("req-1", "approve", "secret-key-32chars-minimum-xxxxx")
	if token == "" {
		t.Error("token should not be empty")
	}
	// Token is base64 encoded — should be longer than the old 64-char hex
	if len(token) < 40 {
		t.Errorf("token too short: %d", len(token))
	}
}

func TestGenerateTokenDifferentActions(t *testing.T) {
	approve := GenerateToken("req-1", "approve", "secret")
	deny := GenerateToken("req-1", "deny", "secret")
	if approve == deny {
		t.Error("different actions should produce different tokens")
	}
}

func TestVerifyTokenValid(t *testing.T) {
	token := GenerateToken("req-1", "approve", "secret")
	id, action, err := VerifyToken(token, "secret", 24)
	if err != nil {
		t.Fatalf("VerifyToken error: %v", err)
	}
	if id != "req-1" {
		t.Errorf("id = %q, want req-1", id)
	}
	if action != "approve" {
		t.Errorf("action = %q, want approve", action)
	}
}

func TestVerifyTokenWrongSecret(t *testing.T) {
	token := GenerateToken("req-1", "approve", "secret")
	_, _, err := VerifyToken(token, "wrong-secret", 24)
	if err == nil {
		t.Error("expected error for wrong secret")
	}
}

func TestVerifyTokenExpired(t *testing.T) {
	// Generate token, then verify with 0 hours expiry — should be expired
	token := GenerateToken("req-1", "approve", "secret")
	// Use -1 expiry hours to simulate expired
	_, _, err := VerifyToken(token, "secret", -1)
	if err == nil {
		t.Error("expected error for expired token")
	}
}

func TestVerifyTokenBadEncoding(t *testing.T) {
	_, _, err := VerifyToken("not-valid-base64!!!", "secret", 24)
	if err == nil {
		t.Error("expected error for bad encoding")
	}
}

func TestVerifyTokenReturnsIDAndAction(t *testing.T) {
	token := GenerateToken("approval-xyz", "deny", "my-secret")
	id, action, err := VerifyToken(token, "my-secret", 24)
	if err != nil {
		t.Fatal(err)
	}
	if id != "approval-xyz" || action != "deny" {
		t.Errorf("got id=%q action=%q", id, action)
	}
}

func TestMultiNotifierDispatch(t *testing.T) {
	m1 := &mockNotifier{}
	m2 := &mockNotifier{}
	mn := &MultiNotifier{channels: []Notifier{m1, m2}}

	mn.Notify(context.Background(), testApproval, "http://approve", "http://deny")

	if m1.notifyCalled.Load() != 1 {
		t.Error("channel 1 should be called once")
	}
	if m2.notifyCalled.Load() != 1 {
		t.Error("channel 2 should be called once")
	}
}

func TestMultiNotifierDecision(t *testing.T) {
	m1 := &mockNotifier{}
	m2 := &mockNotifier{}
	mn := &MultiNotifier{channels: []Notifier{m1, m2}}

	mn.NotifyDecision(context.Background(), testApproval)

	if m1.decisionCalled.Load() != 1 {
		t.Error("channel 1 decision should be called once")
	}
	if m2.decisionCalled.Load() != 1 {
		t.Error("channel 2 decision should be called once")
	}
}

func TestMultiNotifierErrorDoesNotBlock(t *testing.T) {
	failing := &mockNotifier{shouldErr: true}
	working := &mockNotifier{}
	mn := &MultiNotifier{channels: []Notifier{failing, working}}

	mn.Notify(context.Background(), testApproval, "http://approve", "http://deny")

	if working.notifyCalled.Load() != 1 {
		t.Error("working channel should still be called even when other fails")
	}
}

func TestMultiNotifierEmpty(t *testing.T) {
	mn := &MultiNotifier{}
	mn.Notify(context.Background(), testApproval, "http://approve", "http://deny")
	mn.NotifyDecision(context.Background(), testApproval)
}

func TestNewMultiNotifierNoChannelsEnabled(t *testing.T) {
	cfg := config.NotificationsConfig{}
	mn := NewMultiNotifier(cfg, "secret")
	if len(mn.channels) != 0 {
		t.Errorf("no channels enabled, got %d", len(mn.channels))
	}
}

func TestNewMultiNotifierWithChannels(t *testing.T) {
	cfg := config.NotificationsConfig{
		Slack: config.SlackConfig{Enabled: true, WebhookURL: "http://slack"},
		Ntfy:  config.NtfyConfig{Enabled: true, URL: "http://ntfy", Topic: "test"},
	}
	mn := NewMultiNotifier(cfg, "secret")
	if len(mn.channels) != 2 {
		t.Errorf("expected 2 channels, got %d", len(mn.channels))
	}
}

func TestMultiNotifierLen(t *testing.T) {
	empty := NewMultiNotifier(config.NotificationsConfig{}, "secret")
	if empty.Len() != 0 {
		t.Errorf("expected 0 channels on empty config, got %d", empty.Len())
	}

	oneChannel := NewMultiNotifier(config.NotificationsConfig{
		Ntfy: config.NtfyConfig{Enabled: true, URL: "http://localhost:2586", Topic: "test"},
	}, "secret")
	if oneChannel.Len() != 1 {
		t.Errorf("expected 1 channel when Ntfy enabled, got %d", oneChannel.Len())
	}
}

// TestRedactWebhookURLInErrorLogPath verifies that tokens in error strings are properly
// redacted when logged through the notifier's error logging path.
func TestRedactWebhookURLInErrorLogPath(t *testing.T) {
	// Test cases with tokens that should be redacted
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "Telegram bot token in URL",
			err:  fmt.Errorf("telegram API error: Post \"https://api.telegram.org/bot123456:ABC/getUpdates\": dial tcp: lookup api.telegram.org: no such host"),
			want: "[notify] channel error: telegram API error: Post \"https://api.telegram.org/bot<REDACTED>/getUpdates\": dial tcp: lookup api.telegram.org: no such host",
		},
		{
			name: "Discord webhook token in URL",
			err:  fmt.Errorf("failed to send message: Post \"https://discord.com/api/webhooks/123456789/abcdefg_token_here_12345\": dial tcp: lookup discord.com: no such host"),
			want: "[notify] channel error: failed to send message: Post \"https://discord.com/api/webhooks/123456789/<REDACTED>\": dial tcp: lookup discord.com: no such host",
		},
		{
			name: "Slack webhook token in URL",
			err:  fmt.Errorf("slack webhook error: Post \"https://hooks.slack.com/services/T00000000/B00000000/FAKE_SLACK_TOKEN\": context deadline exceeded"),
			want: "[notify] channel error: slack webhook error: Post \"https://hooks.slack.com/services/T00000000/B00000000/<REDACTED>\": context deadline exceeded",
		},
		{
			name: "Bearer token in Authorization header (ntfy)",
			err:  fmt.Errorf("posting to ntfy: Post \"https://ntfy.sh/mytopic\": authorization: Bearer FAKE_BEARER_TOKEN"),
			want: "[notify] channel error: posting to ntfy: Post \"https://ntfy.sh/mytopic\": authorization: Bearer <REDACTED>",
		},
		{
			name: "Bot token in Authorization header (Discord)",
			err:  fmt.Errorf("discord API error: Post \"https://discord.com/api/v10/channels/123456789/messages\": authorization: Bot FAKE_DISCORD_BOT_TOKEN"),
			want: "[notify] channel error: discord API error: Post \"https://discord.com/api/v10/channels/123456789/messages\": authorization: Bot <REDACTED>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture log output by using a mock logger or by checking the error string directly
			// Since we're testing the redaction function's effect on logged errors,
			// we can test the redacted error string directly
			redactedErr := RedactWebhookURLInError(tt.err)
			if redactedErr == nil {
				t.Fatal("expected non-nil error from redaction")
			}

			// Format as it would appear in the log
			logged := fmt.Sprintf("[notify] channel error: %v", redactedErr)

			if logged != tt.want {
				t.Errorf("TestRedactWebhookURLInErrorLogPath(%v) = %q, want %q", tt.err, logged, tt.want)
			}

			// Additionally verify that the original token is not present in the logged output
			if strings.Contains(logged, "123456:ABC") ||
				strings.Contains(logged, "abcdefg_token_here_12345") ||
				strings.Contains(logged, "FAKE_SLACK_TOKEN") ||
				strings.Contains(logged, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9") ||
				strings.Contains(logged, "FAKE_DISCORD_BOT_TOKEN") {
				t.Errorf("original token still present in logged output: %s", logged)
			}
		})
	}
}
