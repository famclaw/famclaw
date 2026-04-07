package skillbridge

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/famclaw/famclaw/internal/honeybadger"
	"github.com/famclaw/famclaw/internal/store"
)

// Quarantine tracks tools blocked by security scans.
// Reads are RWMutex-fast (microsecond map lookup) for the per-turn filter hot path.
// Writes happen only when scans complete (rare).
type Quarantine struct {
	mu      sync.RWMutex
	blocked map[string]QuarantineEntry
	db      *store.DB
}

// QuarantineEntry describes a quarantined tool.
type QuarantineEntry struct {
	ScanTarget string    `json:"scan_target"`
	ToolName   string    `json:"tool_name"`
	Verdict    string    `json:"verdict"`
	Reasoning  string    `json:"reasoning"`
	KeyFinding string    `json:"key_finding"`
	BlockedAt  time.Time `json:"blocked_at"`
}

// NewQuarantine creates a quarantine backed by the given DB.
func NewQuarantine(db *store.DB) *Quarantine {
	return &Quarantine{
		blocked: make(map[string]QuarantineEntry),
		db:      db,
	}
}

// Load reads persisted quarantine entries from the DB on startup.
func (q *Quarantine) Load(_ context.Context) error {
	entries, err := q.db.ListQuarantine()
	if err != nil {
		return fmt.Errorf("loading quarantine: %w", err)
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range entries {
		q.blocked[e.ScanTarget] = QuarantineEntry{
			ScanTarget: e.ScanTarget,
			ToolName:   e.ToolName,
			Verdict:    e.Verdict,
			Reasoning:  e.Reasoning,
			KeyFinding: e.KeyFinding,
			BlockedAt:  e.BlockedAt,
		}
	}
	return nil
}

// IsBlocked is the hot path — must be fast. Called on every turn.
func (q *Quarantine) IsBlocked(scanTarget string) bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	_, ok := q.blocked[scanTarget]
	return ok
}

// Get returns the quarantine entry for a target, if any.
func (q *Quarantine) Get(scanTarget string) (QuarantineEntry, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	e, ok := q.blocked[scanTarget]
	return e, ok
}

// Block adds a tool to the quarantine and persists it.
func (q *Quarantine) Block(_ context.Context, toolName, scanTarget string, result *honeybadger.ScanResult) error {
	entry := QuarantineEntry{
		ScanTarget: scanTarget,
		ToolName:   toolName,
		Verdict:    result.Verdict,
		Reasoning:  result.Reasoning,
		KeyFinding: result.KeyFinding,
		BlockedAt:  time.Now(),
	}

	// Persist first — if DB fails, don't update in-memory
	if err := q.db.UpsertQuarantine(store.QuarantineEntry{
		ScanTarget: entry.ScanTarget,
		ToolName:   entry.ToolName,
		Verdict:    entry.Verdict,
		Reasoning:  entry.Reasoning,
		KeyFinding: entry.KeyFinding,
		BlockedAt:  entry.BlockedAt,
	}); err != nil {
		return fmt.Errorf("persisting quarantine: %w", err)
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	q.blocked[scanTarget] = entry
	return nil
}

// Clear removes a tool from quarantine (e.g. after rescan passes or parent override).
func (q *Quarantine) Clear(_ context.Context, scanTarget string) error {
	if err := q.db.DeleteQuarantine(scanTarget); err != nil {
		return fmt.Errorf("clearing quarantine: %w", err)
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.blocked, scanTarget)
	return nil
}

// List returns all currently quarantined entries.
func (q *Quarantine) List() []QuarantineEntry {
	q.mu.RLock()
	defer q.mu.RUnlock()
	entries := make([]QuarantineEntry, 0, len(q.blocked))
	for _, e := range q.blocked {
		entries = append(entries, e)
	}
	return entries
}

// Len returns the number of quarantined entries.
func (q *Quarantine) Len() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.blocked)
}
