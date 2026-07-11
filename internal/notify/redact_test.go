package notify

import (
	"errors"
	"fmt"
	"net/url"
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
			name: "discord webhook URL with token",
			err:     fmt.Errorf("posting to discord: Post \"https://discord.com/api/webhooks/123456789/FAKE_DISCORD_TOKEN\": dial tcp: connection refused"),
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
				if strings.Contains(gotMsg, "XXXXXXXX") || strings.Contains(gotMsg, "abcdefg_token") || strings.Contains(gotMsg, "FAKE_SLACK_TOKEN") || strings.Contains(gotMsg, "FAKE_DISCORD_TOKEN") || strings.Contains(gotMsg, "FAKE_BEARER_TOKEN") || strings.Contains(gotMsg, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9") {
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

func TestRedactDiscordBotToken(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "Bot token in Authorization header",
			err:  fmt.Errorf("posting to discord: Post \"https://discord.com/api/v10/users/@me\": authorization: Bot FAKE_DISCORD_BOT_TOKEN_LONG_STRING"),
			want: "posting to discord: Post \"https://discord.com/api/v10/users/@me\": authorization: Bot <REDACTED>",
		},
		{
			name: "Bot token with shorter value",
			err:  fmt.Errorf("request failed: authorization: Bot FAKE_DISCORD_BOT_SHORT"),
			want: "request failed: authorization: Bot <REDACTED>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactWebhookURLInError(tt.err)
			if got == nil {
				t.Fatal("expected non-nil error")
			}
			if got.Error() != tt.want {
				t.Errorf("RedactWebhookURLInError(%v) = %q, want %q", tt.err, got.Error(), tt.want)
			}
		})
	}
}

func TestRedactExportedFunction(t *testing.T) {
	// Verify the exported RedactWebhookURLInError produces same output
	// as the unexported redactWebhookURLInError.
	inErr := fmt.Errorf("poll: Get \"https://api.telegram.org/bot111:BBB/sendMessage\": timeout")
	both := []error{
		redactWebhookURLInError(inErr),
		RedactWebhookURLInError(inErr),
	}
	for i, e := range both {
		if e == nil {
			t.Fatalf("redact[%d] returned nil", i)
		}
		if !strings.Contains(e.Error(), "bot<REDACTED>") {
			t.Errorf("redact[%d]: expected bot token redacted, got: %s", i, e.Error())
		}
	}
}

func TestRedactedErrorUnwrap(t *testing.T) {
	inner := &url.Error{Op: "Get", URL: "https://api.telegram.org/bot111:BBB/test", Err: errors.New("timeout")}
	wrapped := fmt.Errorf("wrapper: %w", inner)
	redacted := RedactWebhookURLInError(wrapped)

	if redacted == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(redacted.Error(), "bot<REDACTED>") {
		t.Errorf("expected redaction, got: %s", redacted.Error())
	}

	// Unwrap should return the original error so errors.Is/As still work.
	re, ok := redacted.(*redactedError)
	if !ok {
		t.Fatal("expected *redactedError type assertion")
	}
	unwrapped := re.Unwrap()
	if unwrapped == nil {
		t.Fatal("Unwrap returned nil — error chain is broken")
	}
	if !errors.Is(redacted, inner) {
		t.Error("errors.Is should still match the original wrapped error")
	}
}

func TestRedactTelegramWithMethodPath(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "sendMessage with token in URL",
			err:  fmt.Errorf("sending message: Post \"https://api.telegram.org/bot123456:ABCxyz/sendMessage\": connection refused"),
			want: "sending message: Post \"https://api.telegram.org/bot<REDACTED>/sendMessage\": connection refused",
		},
		{
			name: "getUpdates with method path",
			err:  fmt.Errorf("poll: Get \"https://api.telegram.org/botTOKEN123/getUpdates?offset=0\": dial tcp: no route to host"),
			want: "poll: Get \"https://api.telegram.org/bot<REDACTED>/getUpdates?offset=0\": dial tcp: no route to host",
		},
		{
			name: "sendPhoto with nested path",
			err:  fmt.Errorf("telegram API error: {\"ok\":false,\"error_code\":400,\"description\":\"Bad Request: chat not found\"}"),
			want: "telegram API error: {\"ok\":false,\"error_code\":400,\"description\":\"Bad Request: chat not found\"}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactWebhookURLInError(tt.err)
			if got == nil {
				t.Fatal("expected non-nil error")
			}
			gotMsg := got.Error()
			if gotMsg != tt.want {
				t.Errorf("got  %q\nwant %q", gotMsg, tt.want)
			}
		})
	}
}
