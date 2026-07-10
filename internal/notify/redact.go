package notify

import (
	"regexp"
)

// slackServiceRe matches the token segment in Slack webhook URLs.
var slackServiceRe = regexp.MustCompile("(https://hooks" + `\.slack\.com/services/[^/]+/[^/]+/)[^"/]+`)

// discordWebhookRe matches the token segment in Discord webhook URLs.
// Format: https://discord.com/api/webhooks/<id>/<token>
var discordWebhookRe = regexp.MustCompile(`(https://discord\.com/api/webhooks/\d+/)[^"/]+`)

// ntfyAuthRe matches Bearer tokens in Authorization header strings that may
// appear in error messages (e.g. when a client library stringifies a request).
var ntfiAuthRe = regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9\-_\.]+`)

// redactWebhookURLInError replaces any webhook URL tokens visible in the error
// chain with <REDACTED>. It only affects the string representation of errors.
func redactWebhookURLInError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	msg = slackServiceRe.ReplaceAllString(msg, "${1}<REDACTED>")
	msg = discordWebhookRe.ReplaceAllString(msg, "${1}<REDACTED>")
	// ntfy uses a Bearer token in the Authorization header, not in the URL.
	// If the error message contains "Bearer <token>", redact it.
	msg = ntfiAuthRe.ReplaceAllString(msg, "${1}<REDACTED>")
	if msg != err.Error() {
		return &redactedError{msg: msg}
	}
	return err
}

// redactedError wraps an error with a redacted message string.
type redactedError struct {
	msg string
}

func (e *redactedError) Error() string   { return e.msg }
func (e *redactedError) Unwrap() error   { return nil }
