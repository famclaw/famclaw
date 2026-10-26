package mcpserver

import (
	"fmt"
	"regexp"
)

// credentialRe matches content that looks like a credential, one-time code,
// or passcode being typed into a device (design §5.6 hard-stop). The match
// is intentionally over-eager: a false positive (refusing to type the word
// "pin" in a sentence) is far safer than a false negative (typing a real
// 6-digit OTP into a smart-home app).
var credentialRe = regexp.MustCompile(
	`(?i)(\b\d{6,8}\b` + // 6–8 consecutive digits (OTP / PIN / passcode)
		`|\bpassword\b` +
		`|\bpasscode\b` +
		`|\bpin\b` +
		`|\botp\b` +
		`|\bsecret\b` +
		`|\btoken\b` +
		`|one[- ]?time[- ]?code` +
		`|verification[- ]?code` +
		`|security[- ]?code` +
		`|2fa\b)`,
)

// checkCredentialContent returns an error when text looks like credential
// material that the butler must never enter (design §5.6).
func checkCredentialContent(text string) error {
	if credentialRe.MatchString(text) {
		return fmt.Errorf("refusing to type credential/OTP/passcode-shaped content")
	}
	return nil
}

// destructiveRe matches shell commands that destroy device data. The shell
// tool is operator-only and already behind a config kill-switch; this is the
// second, content-level gate (design §5.5: "refuse destructive commands").
var destructiveRe = regexp.MustCompile(
	`(?i)(\bformat\s+/` + // format /data, /system, etc.
		`|\bpm\s+clear\b` + // clear app data
		`|\bpm\s+uninstall\b` + // uninstall app
		`|\bwm\s+size\s+reset\b` +
		`|\bsettings\s+put\s+secure\s+lock_pattern_autolock\b` +
		`|\bshutdown\b` +
		`|\breboot\b)`,
)

// checkShellDestructive returns an error when a shell command matches a
// known-destructive pattern.
func checkShellDestructive(cmd string) error {
	if destructiveRe.MatchString(cmd) {
		return fmt.Errorf("refusing destructive shell command")
	}
	return nil
}
