// Package webfilter provides the bundled kid-safe baseline blocklist and
// (in a future W4) the offline suspicious-host detector. The baseline is
// a CC0-sourced list of known kid-unsafe domains, embedded at compile time
// via go:embed so it works with zero network access (RPi / flaky-internet
// target, design §2.2).
package webfilter

import (
	"embed"
	"strings"
	"sync"
)

//go:embed baseline.txt
var baselineFS embed.FS

var (
	once     sync.Once
	baseline map[string]struct{} // canonical host → struct{}
)

// BaselineBlocks reports whether the given host matches an entry in the
// bundled kid-safe baseline. Matching is case-insensitive and supports
// subdomain-suffix matching (same semantics as url_allowlist / url_denylist):
// if "example.com" is in the baseline, "sub.example.com" also matches.
//
// The host should already be canonicalized (lowercase, no trailing dot,
// no port) — the caller is responsible for calling canonicalizeHost first.
func BaselineBlocks(host string) bool {
	once.Do(func() {
		baseline = loadBaseline()
	})
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	// Exact match.
	if _, ok := baseline[host]; ok {
		return true
	}
	// Subdomain suffix: "sub.example.com" matches baseline "example.com".
	// Peel the leftmost label each step so every parent domain is checked:
	// "www.omegle.com" -> "omegle.com" -> "com".
	candidate := host
	for {
		idx := strings.IndexByte(candidate, '.')
		if idx < 0 {
			break
		}
		candidate = candidate[idx+1:]
		if _, ok := baseline[candidate]; ok {
			return true
		}
	}
	return false
}

// BaselineCount returns the number of unique hosts in the bundled baseline.
// Useful for admin tools to report "113 baseline hosts active".
func BaselineCount() int {
	once.Do(func() {
		baseline = loadBaseline()
	})
	return len(baseline)
}

func loadBaseline() map[string]struct{} {
	data, err := baselineFS.ReadFile("baseline.txt")
	if err != nil {
		// Should never happen in production (embed is compile-time);
		// return empty set to fail-open (baseline is a safety layer,
		// an empty set means "no baseline blocks" which is less safe
		// than a broken build).
		return map[string]struct{}{}
	}
	set := make(map[string]struct{}, 128)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Normalize: lowercase, strip trailing dot, strip port.
		host := strings.ToLower(line)
		if idx := strings.LastIndexByte(host, ':'); idx > 0 {
			host = host[:idx]
		}
		host = strings.TrimSuffix(host, ".")
		if host != "" {
			set[host] = struct{}{}
		}
	}
	return set
}
