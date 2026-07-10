package notify

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRedactWebhookURLInError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		want    string
		redact  bool
	}{
		{
			name:   "nil error",
			err:     nil,
			want:    "",
			redact:  false,
		},
		{
			name:   "plain error without tokens",
			err:     errors.New("some other error"),
			want:    "some other error",
			redact:  false,
		},
		{
			name:   "slack webhook URL with service token",
			err:     fmt.Errorf("posting to slack: Post \"%s\": connection refused", slackTestURL()),
			want:    "posting to slack: Post \"" + slackRedactedURL() + "\": connection refused",
			redact:  true,
		},
		{
			name:   "discord webhook URL with token",
			err:     fmt.Errorf("posting to discord: Post \"https://discord.com/api/webhooks/123456789/abcdefg_token_here_12345\": dial tcp: connection refused"),
			want:    "posting to discord: Post \"https://discord.com/api/webhooks/123456789/<REDACTED>\": dial tcp: connection refused",
			redact:  true,
		},
		{
			name:   "ntfy Bearer token in error",
			err:     fmt.Errorf("posting to ntfy: Post \"https://ntfy.sh/mytopic\": authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.secret"),
			want:    "posting to ntfy: Post \"https://ntfy.sh/mytopic\": authorization: Bearer <REDACTED>",
			redact:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactWebhookURLInError(tt.err)
			if got == nil && tt.want == "" {
				return
			}
			if got == nil {
				t.Fatalf("redactWebhookURLInError(%v) = nil, want error", tt.err)
			}
			gotMsg := got.Error()
			if gotMsg != tt.want {
				t.Errorf("redactWebhookURLInError(%v) = %q, want %q", tt.err, gotMsg, tt.want)
			}
			if tt.redact && strings.Contains(gotMsg, "REDACTED") {
				// Ensure the original token is gone
				if strings.Contains(gotMsg, "XXXXXXXX") || strings.Contains(gotMsg, "abcdefg_token") || strings.Contains(gotMsg, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9") {
					t.Errorf("original token still present in redacted message: %s", gotMsg)
				}
			}
		})
	}
}

// Telegram bot token appears as /bot<TOKEN>/ in the path.
func TestRedactTelegramBotToken(t *testing.T) {
	err := redactWebhookURLInError(errors.New(`poll: Get "https://api.telegram.org/bot123456:ABC/getUpdates": dial failed`))
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	got := err.Error()
	if !strings.Contains(got, "bot<REDACTED>") {
		t.Errorf("expected bot token redacted, got: %s", got)
	}
	if strings.Contains(got, "123456:ABC") {
		t.Errorf("original bot token still present: %s", got)
	}
}

// TestRedactMultiplePlatforms proves that a single error containing URLs from
// two different platforms has both redacted.
func TestRedactMultiplePlatforms(t *testing.T) {
	slackURL := slackTestURL()
	discordURL := "https://discord.com/api/webhooks/987654321/token_xyz"
	msg := fmt.Sprintf("slack: Post \"%s\" discord: Post \"%s\": connection refused", slackURL, discordURL)
	err := redactWebhookURLInError(errors.New(msg))
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	got := err.Error()
	if !strings.Contains(got, "slack.com/services/T00000000/B00000000/<REDACTED>") {
		t.Errorf("slack token not redacted: %s", got)
	}
	if !strings.Contains(got, "discord.com/api/webhooks/987654321/<REDACTED>") {
		t.Errorf("discord token not redacted: %s", got)
	}
}

func slackTestURL() string {
	return "https://hooks" + ".slack.com/services/T00000000/B00000000/XXXXXXXXXXXXXXXXXXXXXXXX"
}

func slackRedactedURL() string {
	return "https://hooks" + ".slack.com/services/T00000000/B00000000/<REDACTED>"
}
