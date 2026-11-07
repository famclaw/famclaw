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
	"unicode"

	"golang.org/x/net/idna"
)

//go:embed baseline.txt
var baselineFS embed.FS

// Constant reference data for the offline heuristics, hoisted to package
// level so per-call host checks don't reallocate it (project rule:
// preallocate / avoid repeated allocations in hot paths).
var (
	knownGoodHosts = []string{
		"google.com", "facebook.com", "youtube.com", "amazon.com",
		"twitter.com", "instagram.com", "linkedin.com", "wikipedia.org",
		"reddit.com", "netflix.com", "spotify.com", "ebay.com",
		"microsoft.com", "apple.com", "github.com", "stackoverflow.com",
	}
	standardPorts = map[string]bool{"80": true, "443": true}
	// confusableChars lists the homoglyphs the detector hunts for in the
	// host. Entries are the POST-LOWERCASE forms: the sole production
	// caller (agent.hostDecision) canonicalizes the host with
	// strings.ToLower before Check, so the list must hold what each
	// confusable looks like after that canonicalization — not raw input
	// forms. (Raw-form entries were dead weight: '\u0391' Greek capital A
	// canonicalizes to '\u03B1' before the loop ever sees it, and the
	// preceding IDN check does not cover it, because idna.ToASCII
	// succeeds on Greek hosts and the check then breaks without
	// flagging. '\u212A' Kelvin sign K canonicalizes to ASCII 'k' and is
	// indistinguishable from a legitimate 'k' post-lowercase; its
	// multi-edit leet variants remain covered by the curated kid-safe
	// baseline and parent denylist per the B2 trade-off documented in
	// isTypoSquat.)
	confusableChars = []rune{'\u0430', '\u03B1'} // Cyrillic 'a', Greek 'α' (post-lowercase)
)

// Verdict represents the result of a suspicious site check.
type Verdict struct {
	Action string // "block", "ask_parent", or "warn_allow"
	Reason string // human-readable explanation
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
	// IP literals: under the home-LAN model (design §1.5) private, loopback
	// and link-local endpoints (SearXNG, NAS, local services) are permitted
	// by default — raw-IP security for those ranges is the dialer gate's job.
	// A raw PUBLIC IP literal is not a domain: the domain-based baseline and
	// parent denylist cannot match it, so a child could reach a
	// baseline-blocked site via its public IP. Public IPs are therefore
	// routed to the parent-approval gate instead of being silently allowed.
	if isIP, local := classifyIPLiteral(host); isIP {
		if local {
			return Verdict{
				Action: "warn_allow",
				Reason: "private/loopback/link-local IP literal permitted by default (home-LAN model)",
			}
		}
		return Verdict{
			Action: "ask_parent",
			Reason: "public IP literal host",
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
	isIP, _ := classifyIPLiteral(host)
	return isIP
}

// classifyIPLiteral reports whether host is an IP literal (bare, bracketed,
// or host:port) and whether it falls in a home-LAN range (private, loopback,
// or link-local). This keeps the "which IPs are local" logic in one place so
// the detector and the agent gate agree.
func classifyIPLiteral(host string) (isIP, local bool) {
	var ipStr string
	switch {
	case strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]"):
		ipStr = host[1 : len(host)-1]
	default:
		if h, _, err := net.SplitHostPort(host); err == nil {
			ipStr = h
		} else {
			ipStr = host
		}
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false, false
	}
	local = ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
	return true, local
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
	// Bare IP literals (e.g. "::1", "192.168.1.50") carry no port information
	// in the host string; return early to avoid mis-parsing colons in IPv6.
	// Bracketed IPv6 with explicit port ([::1]:8888) is handled correctly
	// by net.SplitHostPort below.
	if net.ParseIP(host) != nil {
		return false
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
//
// B2 tightened the kid fail-safe: any host within Levenshtein distance
// <= 1 of a known-good host (e.g. "googl.com" vs "google.com") is now
// flagged (previously <= 2). The single-edit bound keeps real typo-squats
// such as "gogle.com" / "facebok.com" in the ask_parent path while dropping
// the two-edit surface that auto-blocked legitimate near-misses ("g00gle.com")
// for up to 24 h. The TLD-shape guard below still applies.
//
// Tradeoff (documented, PM decision 2026-10-31): a distance-2 leet variant
// such as "faceb00k.com" is consequently no longer auto-flagged here. Such
// squats are covered instead by the bundled kid-safe baseline (the repo's
// default URL denylist, which hostDecision consults before any detector
// verdict; "faceb00k.com" is a curated entry there, marked as a known
// typo-squat of facebook.com), and by the per-install parent URLDenylist for
// hosts the parent wants blocked.
//
// In agent mode a flagged host routes to ask_parent - a parent-approval row is
// created and the fetch is held for a human - rather than a hard block. The
// per-fetch cost (an approval lookup + upsert) is bounded by the 24h approval
// expiry, so repeated lookups stop mattering after one day.
func isTypoSquat(host string) bool {
	// Early return for known good hosts
	for _, knownHost := range knownGoodHosts {
		if host == knownHost {
			return false
		}
	}

	// Check for similarity with known good hosts (distance ≤ 1)
	for _, knownHost := range knownGoodHosts {
		if isSimilar(host, knownHost) {
			// Tighten so Levenshtein ≤ 1 only flags when TLD matches AND candidate ≠ known host
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

// isSimilar checks if two hosts are similar (with edit distance ≤ 1).
func isSimilar(host1, host2 string) bool {
	// Convert to lowercase for comparison
	host1 = strings.ToLower(host1)
	host2 = strings.ToLower(host2)

	// Calculate edit distance (simplified)
	distance := editDistance(host1, host2)
	return distance <= 1
}

// editDistance calculates the Levenshtein distance between two strings.
func editDistance(s1, s2 string) int {
	// Convert strings to rune slices for proper Unicode handling
	r1 := []rune(s1)
	r2 := []rune(s2)

	// Early exit: Levenshtein distance >= |len(r1) - len(r2)|. The sole caller
	// (isSimilar) only checks distance <= 1, so a length gap greater than 1
	// proves the hosts are not similar and lets us skip the O(n*m) matrix.
	if d := len(r1) - len(r2); d > 1 {
		return d
	} else if d < -1 {
		return -d
	}

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
				m[i+1][j+1] = min3(m[i][j+1]+1, m[i+1][j]+1, m[i][j]+1)
			}
		}
	}

	return m[len(r1)][len(r2)]
}

// Helper function to find minimum of three integers
func min3(a, b, c int) int {
	minVal := a
	if b < minVal {
		minVal = b
	}
	if c < minVal {
		minVal = c
	}
	return minVal
}

// BaselineStore holds the bundled kid-safe baseline blocklist. It is loaded
// once at construction from the embedded baseline.txt and is read-only
// thereafter, so no sync.Once / package-level global is needed (project rule:
// no global state — everything is injected via constructor).
type BaselineStore struct {
	blocks map[string]struct{} // canonical host → struct{}
}

// NewBaselineStore loads the embedded baseline blocklist. A load failure
// fails open (empty set), matching the previous behavior.
func NewBaselineStore() *BaselineStore {
	return &BaselineStore{blocks: loadBaseline()}
}

// Blocks reports whether host matches a baseline entry. Matching is
// case-insensitive and supports subdomain-suffix matching (same semantics as
// url_allowlist / url_denylist): if "example.com" is in the baseline,
// "sub.example.com" also matches. host should already be canonicalized
// (lowercase, no trailing dot, no port).
func (s *BaselineStore) Blocks(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	// Exact match.
	if _, ok := s.blocks[host]; ok {
		return true
	}
	// Subdomain suffix: peel the leftmost label each step so every parent
	// domain is checked: "www.omegle.com" -> "omegle.com" -> "com".
	candidate := host
	for {
		idx := strings.IndexByte(candidate, '.')
		if idx < 0 {
			break
		}
		candidate = candidate[idx+1:]
		if _, ok := s.blocks[candidate]; ok {
			return true
		}
	}
	return false
}

// Count returns the number of unique hosts in the bundled baseline.
func (s *BaselineStore) Count() int {
	return len(s.blocks)
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
	// Split once: the entry count is also the exact map preallocation
	// hint, so a baseline larger than the old 128 hint no longer forces
	// regrowth on every insertion (review finding: unbounded hint).
	lines := strings.Split(string(data), "\n")
	set := make(map[string]struct{}, len(lines))
	for _, line := range lines {
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
