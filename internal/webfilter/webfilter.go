// Package webfilter provides the bundled kid-safe baseline blocklist and
// the offline suspicious-host detector. The baseline is
// a CC0-sourced list of known kid-unsafe domains, embedded at compile time
// via go:embed so it works with zero network access (RPi / flaky-internet
// target, design §2.2).
package webfilter

import (
	"embed"
	"net"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/net/idna"
)

//go:embed baseline.txt
var baselineFS embed.FS

var (
	once     sync.Once
	baseline map[string]struct{} // canonical host → struct{}
)

// Verdict represents the result of a suspicious site check.
type Verdict struct {
	Action  string // "block", "ask_parent", or "warn_allow"
	Reason  string // human-readable explanation
}

// SuspiciousSiteDetector implements the offline suspicious site checking logic.
// It performs heuristics to detect potentially malicious or suspicious sites
// without making any network calls.
type SuspiciousSiteDetector struct{}

// NewSuspiciousSiteDetector creates a new instance of the suspicious site detector.
func NewSuspiciousSiteDetector() *SuspiciousSiteDetector {
	return &SuspiciousSiteDetector{}
}

// Check determines if a host is suspicious based on various heuristics.
// Returns a Verdict indicating whether to block, ask parent, or warn_allow.
func (s *SuspiciousSiteDetector) Check(host string) Verdict {
	// Check if host is an IP literal (IPv4 or IPv6)
	if isIPLiteral(host) {
		return Verdict{
			Action: "block",
			Reason: "IP literal host detected",
		}
	}

	// Check for non-standard ports
	if hasNonStandardPort(host) {
		return Verdict{
			Action: "ask_parent",
			Reason: "non-standard port detected",
		}
	}

	// Check for punycode/IDN/confusables
	if hasPunycodeOrConfusables(host) {
		return Verdict{
			Action: "ask_parent",
			Reason: "punycode or confusable characters detected",
		}
	}

	// Check for high-entropy single label (≥24 chars)
	if hasHighEntropySingleLabel(host) {
		return Verdict{
			Action: "ask_parent",
			Reason: "high entropy single label detected",
		}
	}

	// Check for typo-squatting to known-good hosts
	if isTypoSquat(host) {
		return Verdict{
			Action: "ask_parent",
			Reason: "typo-squatting detected",
		}
	}

	// If none of the above conditions are met, allow with warning
	return Verdict{
		Action: "warn_allow",
		Reason: "host passed suspicious site checks",
	}
}

// isIPLiteral checks if the host is an IP literal (IPv4 or IPv6).
func isIPLiteral(host string) bool {
	// Handle IPv6 bracketed notation like [::1]
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		// Extract the IP part inside brackets
		ipStr := host[1 : len(host)-1]
		ip := net.ParseIP(ipStr)
		return ip != nil
	}

	// Handle IPv4 and IPv6 with ports
	// Use net.SplitHostPort for proper port-aware parsing
	if _, _, err := net.SplitHostPort(host); err == nil {
		// Valid host:port format, check if the host part is an IP literal
		hostWithoutPort, _, _ := net.SplitHostPort(host)
		ip := net.ParseIP(hostWithoutPort)
		return ip != nil
	} else {
		// If we can't split, check if it's a direct IP literal
		ip := net.ParseIP(host)
		return ip != nil
	}
}

// hasNonStandardPort checks if the host has a non-standard port.
// Standard ports are the two well-known HTTP(S) ports only; everything
// else (including 8080/8443) is surfaced to the parent per design §4.
//
// Reachability note: in the agent's host-gate flow the host reaches Check()
// via url.URL.Hostname(), which strips the port, so this check does not fire
// on that path. That is intentional, not a bug: the home-LAN model (design
// §1.5) must let self-hosted services on non-standard ports (e.g. SearXNG on
// :8888) pass without a parent prompt, and gating on the port would break
// exactly those. The check is kept (and unit-tested) for any direct
// Check(host:port) caller that passes a raw host:port string.
func hasNonStandardPort(host string) bool {
	// Standard ports for HTTP/HTTPS
	standardPorts := map[string]bool{
		"80":  true,
		"443": true,
	}

	// Extract port if present
	port := ""
	
	// Use net.SplitHostPort to properly handle ports
	if _, portPart, err := net.SplitHostPort(host); err == nil {
		// If we successfully split, check if port is standard
		if _, err := strconv.Atoi(portPart); err == nil {
			port = portPart
		}
	} else {
		// If we can't split, check for port in the host string
		if idx := strings.LastIndex(host, ":"); idx > 0 {
			// Check if the part after ':' is a valid port number
			portPart := host[idx+1:]
			if _, err := strconv.Atoi(portPart); err == nil {
				port = portPart
			}
		}
	}

	// If port is not standard, ask parent
	return port != "" && !standardPorts[port]
}

// hasPunycodeOrConfusables checks if the host contains punycode or confusable characters.
func hasPunycodeOrConfusables(host string) bool {
	// Check for punycode prefixes
	if strings.Contains(host, "xn--") {
		return true
	}

	// Check for IDN characters (non-ASCII)
	for _, r := range host {
		if r > unicode.MaxASCII {
			// Try to decode IDN to see if it's valid
			_, err := idna.ToASCII(host)
			if err != nil {
				return true
			}
			break
		}
	}

	// Check for potential confusables (this is simplified)
	// In practice, we might want to use a more comprehensive check
	confusableChars := []rune{'\u0430', '\u0391', '\u212A'} // Cyrillic 'a', Greek 'A', Kelvin sign
	for _, r := range host {
		for _, c := range confusableChars {
			if r == c {
				return true
			}
		}
	}

	return false
}

// hasHighEntropySingleLabel checks if any single label of the host is long
// enough (≥24 chars) to look like a machine-generated token (raw hash,
// obfuscated name, C2-style domain). Length-based on purpose: the detector
// stays offline and deterministic, and the threshold — not Shannon
// entropy — is what distinguishes generated labels from ordinary names.
// Multi-label hosts are NOT excluded: "a24charlabel…​.com" is exactly the
// shape the heuristic targets.
func hasHighEntropySingleLabel(host string) bool {
	for _, label := range strings.Split(host, ".") {
		if len(label) >= 24 {
			return true
		}
	}
	return false
}

// isTypoSquat checks if the host is a typo-squatting attempt.
func isTypoSquat(host string) bool {
	// Add early-return for known hosts
	knownGoodHosts := []string{
		"google.com", "facebook.com", "youtube.com", "amazon.com", 
		"twitter.com", "instagram.com", "linkedin.com", "wikipedia.org",
		"reddit.com", "netflix.com", "spotify.com", "ebay.com",
		"microsoft.com", "apple.com", "github.com", "stackoverflow.com",
	}
	
	// Early return for known good hosts
	for _, knownHost := range knownGoodHosts {
		if host == knownHost {
			return false
		}
	}

	// Check for similarity with known good hosts (distance ≤ 2)
	for _, knownHost := range knownGoodHosts {
		if isSimilar(host, knownHost) {
			// Tighten so Levenshtein ≤ 2 only flags when TLD matches AND candidate ≠ known host
			hostParts := strings.Split(host, ".")
			knownHostParts := strings.Split(knownHost, ".")
			
			// If TLDs don't match, don't flag as typo-squat
			if len(hostParts) > 1 && len(knownHostParts) > 1 && hostParts[len(hostParts)-1] != knownHostParts[len(knownHostParts)-1] {
				continue
			}
			
			return true
		}
	}

	return false
}

// isSimilar checks if two hosts are similar (with edit distance ≤ 2).
func isSimilar(host1, host2 string) bool {
	// Convert to lowercase for comparison
	host1 = strings.ToLower(host1)
	host2 = strings.ToLower(host2)

	// Calculate edit distance (simplified)
	distance := editDistance(host1, host2)
	return distance <= 2
}

// editDistance calculates the Levenshtein distance between two strings.
func editDistance(s1, s2 string) int {
	// Convert strings to rune slices for proper Unicode handling
	r1 := []rune(s1)
	r2 := []rune(s2)

	// Create matrix
	m := make([][]int, len(r1)+1)
	for i := range m {
		m[i] = make([]int, len(r2)+1)
	}

	// Initialize matrix
	for i := 0; i <= len(r1); i++ {
		m[i][0] = i
	}
	for j := 0; j <= len(r2); j++ {
		m[0][j] = j
	}

	// Fill matrix
	for i, r1Char := range r1 {
		for j, r2Char := range r2 {
			if r1Char == r2Char {
				m[i+1][j+1] = m[i][j]
			} else {
				m[i+1][j+1] = min(m[i][j+1]+1, m[i+1][j]+1, m[i][j]+1)
			}
		}
	}

	return m[len(r1)][len(r2)]
}

// Helper function to find minimum of three integers
func min(a, b, c int) int {
	minVal := a
	if b < minVal {
		minVal = b
	}
	if c < minVal {
		minVal = c
	}
	return minVal
}

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