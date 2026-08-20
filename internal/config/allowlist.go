package config

import (
	"fmt"
	"net/url"
	"strings"
)

// NormalizeAllowlistHost canonicalizes a user-supplied URL-allowlist entry so
// that every write path (web dashboard, chat tool, config file edits) stores
// the same form the web_fetch host gate compares against.
//
// It accepts a bare host ("example.com"), a case variant ("EXAMPLE.COM."),
// and full URLs ("https://example.com:8443/page") and returns the bare
// lowercase host with port, path, and trailing dot stripped. This mirrors
// the canonicalHost logic in the agent's allowlist gate, which matches
// u.Hostname() — an entry that still carries a scheme or port would never
// match and would silently stay blocked.
//
// The result is validated as a plausible hostname (RFC 1123 labels, or an
// IPv4 literal). Entries that cannot be normalized are rejected with an
// error so a bad entry is never "saved" and left mysteriously unmatchable.
func NormalizeAllowlistHost(s string) (string, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", fmt.Errorf("empty host")
	}

	host := strings.ToLower(trimmed)
	// url.Parse needs a scheme; prefix one when the user typed a bare host.
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("invalid host %q: %w", s, err)
	}

	h := u.Hostname() // strips port, user:info, brackets
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return "", fmt.Errorf("host %q has no host part", s)
	}

	// IPv4 literals: Hostname() returns "192.168.1.10" — allow all-numeric
	// labels. Everything else must be a dot-separated sequence of valid
	// RFC 1123 labels (letters, digits, hyphens; no leading/trailing hyphen).
	if isIPv4Literal(h) {
		return h, nil
	}
	for _, label := range strings.Split(h, ".") {
		if !isHostLabel(label) {
			return "", fmt.Errorf("invalid host %q: %q is not a valid hostname", s, label)
		}
	}
	return h, nil
}

func isIPv4Literal(h string) bool {
	parts := strings.Split(h, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func isHostLabel(label string) bool {
	if label == "" || len(label) > 63 {
		return false
	}
	if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}
	for _, c := range label {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
