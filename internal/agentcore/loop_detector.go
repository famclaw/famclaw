package agentcore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// ActionLoopDetector tracks recently executed tool calls within a single turn
// to spot the agent repeating the same action without making progress.
//
// It is intentionally NON-BLOCKING — Push always succeeds and the action
// always runs. Nudge() returns an English-language hint to inject into the
// next iteration's user message at increasing severity levels (5/8/12 reps
// in the rolling window). Inspired by browser-use's agent/views.py loop
// detector.
type ActionLoopDetector struct {
	window []string // SHA-256[:12] hashes of normalized actions
	cap    int      // rolling-window capacity; default 20
}

// NewActionLoopDetector returns a detector with the given rolling-window cap.
// cap <= 0 defaults to 20.
func NewActionLoopDetector(cap int) *ActionLoopDetector {
	if cap <= 0 {
		cap = 20
	}
	return &ActionLoopDetector{cap: cap}
}

// Push records one executed tool call. action is the tool name; args is the
// raw argument map. The detector hashes (action, normalized-args) and pushes
// to the rolling window, evicting the oldest when full.
func (d *ActionLoopDetector) Push(action string, args map[string]any) {
	// Defensive default: a zero-value ActionLoopDetector (cap == 0) used
	// without NewActionLoopDetector would otherwise panic here — with
	// cap 0, the eviction branch slices an empty window. Default to 20.
	if d.cap <= 0 {
		d.cap = 20
	}
	jsonArgs, err := json.Marshal(args)
	if err != nil {
		jsonArgs = []byte(fmt.Sprintf("%v", args))
	}
	raw := []byte(action + "\x00" + string(jsonArgs))
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])[:12]

	if len(d.window) >= d.cap {
		d.window = d.window[1:]
	}
	d.window = append(d.window, hash)
}

// Nudge returns the prose nudge for the next user message, or "" if no
// repetition pattern is currently triggering. The thresholds are:
//
//	>= 12 reps of the same hash:
//	  "STOP. You have done this same action 12 times. Try a fundamentally different approach (different site, different element, or stop and report what you have found)."
//	>= 8 reps:
//	  "You're repeating the same action. Reconsider — call browser_extract to read the page text, or browser_snapshot for a fresh ref table."
//	>= 5 reps:
//	  "If this repetition is intentional, fine. Otherwise reconsider your approach."
//
// Returns the highest-severity matching message, or "" if no threshold met.
func (d *ActionLoopDetector) Nudge() string {
	counts := make(map[string]int, len(d.window))
	for _, h := range d.window {
		counts[h]++
	}
	maxCount := 0
	for _, c := range counts {
		if c > maxCount {
			maxCount = c
		}
	}
	switch {
	case maxCount >= 12:
		return "STOP. You have done this same action 12 times. Try a fundamentally different approach (different site, different element, or stop and report what you have found)."
	case maxCount >= 8:
		return "You're repeating the same action. Reconsider — call browser_extract to read the page text, or browser_snapshot for a fresh ref table."
	case maxCount >= 5:
		return "If this repetition is intentional, fine. Otherwise reconsider your approach."
	default:
		return ""
	}
}
