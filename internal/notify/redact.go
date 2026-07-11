package notify

import (
	"net/url"
	"regexp"
	"strings"
)

// urlInError matches a quoted URL inside a Go error message string.
var urlInError = regexp.MustCompile(`"(https?://[^"]+)"`)

// bearerToken matches Bearer/Bot auth tokens that appear in error messages.
// ntfy (and other services) embed credentials in Authorization headers,
// not in the URL, so a regex is appropriate here. Discord uses "Bot <token>"
// in the Authorization header which can leak into error strings.
var bearerToken = regexp.MustCompile(`(?i)(Bearer\s+|Bot\s+)[A-Za-z0-9\-_\.]+`)

// redactFn is a URL-redaction function keyed by hostname.
type redactFn func(*url.URL)

// webhookRedactors maps known webhook hosts to their redaction functions.
var webhookRedactors = map[string]redactFn{
	"hooks.slack.com": redactSlack,
	"discord.com":     redactDiscord,
	"api.telegram.org": redactTelegram,
}

// redactSlack redacts the token segment from a Slack service URL.
// Format: https://hooks.slack.com/services/T<CH_ID>/B<BOT_ID>/<TOKEN>
func redactSlack(u *url.URL) {
	parts := strings.Split(u.Path, "/")
	if len(parts) == 5 && parts[1] == "services" {
		parts[4] = "<REDACTED>"
		u.Path = strings.Join(parts, "/")
	}
}

// redactDiscord redacts the token segment from a Discord webhook URL.
// Format: https://discord.com/api/webhooks/<id>/<token>
func redactDiscord(u *url.URL) {
	parts := strings.Split(u.Path, "/")
	if len(parts) == 5 && parts[2] == "webhooks" {
		parts[4] = "<REDACTED>"
		u.Path = strings.Join(parts, "/")
	}
}

// redactTelegram redacts the bot token from a Telegram API URL.
// Format: https://api.telegram.org/bot<TOKEN>/...
func redactTelegram(u *url.URL) {
	if strings.HasPrefix(u.Path, "/bot") {
		rest := u.Path[4:] // strip "/bot"
		if idx := strings.Index(rest, "/"); idx > 0 {
			u.Path = "/bot" + "<REDACTED>" + rest[idx:]
		} else {
			u.Path = "/bot<REDACTED>"
		}
	}
}

// RedactWebhookURLInError replaces any webhook URL tokens visible in the error
// chain with <REDACTED>. It only affects the string representation of errors
// and is safe to call at the log/return boundary.
func RedactWebhookURLInError(err error) error {
	return redactWebhookURLInError(err)
}

// redactWebhookURLInError replaces any webhook URL tokens visible in the error
// chain with <REDACTED>. It only affects the string representation of errors.
func redactWebhookURLInError(err error) error {
	if err == nil {
		return nil
	}

	msg := err.Error()

	// Redact tokens in webhook URLs using url.Parse + host switch.
	msg = urlInError.ReplaceAllStringFunc(msg, func(m string) string {
		raw := m[1 : len(m)-1] // strip quotes
		u, err := url.Parse(raw)
		if err != nil {
			return m
		}
		if fn, ok := webhookRedactors[u.Host]; ok {
			fn(u)
		}
		// Reconstruct without quoting — u.String() would escape < > as %3C %3E.
		var rebuilt string
		if u.RawQuery != "" {
			rebuilt = `"` + u.Scheme + `://` + u.Host + u.Path + `?` + u.RawQuery + `"`
		} else {
			rebuilt = `"` + u.Scheme + `://` + u.Host + u.Path + `"`
		}
		return rebuilt
	})

	// Redact Bearer tokens (ntfy, etc.) — these live in Authorization
	// headers that get stringified into error messages, not in the URL.
	msg = bearerToken.ReplaceAllString(msg, "${1}<REDACTED>")

	if msg != err.Error() {
		return &redactedError{msg: msg, orig: err}
	}
	return err
}

// redactedError wraps an error with a redacted message string.
type redactedError struct {
	msg  string
	orig error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.orig }
