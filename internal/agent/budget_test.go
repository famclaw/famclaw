package agent

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/toolcache"
)

// TestComputeHeadBudget verifies the head-budget formula and its absolute
// floor/ceiling. Expected values are computed by calling HeadBudgetForContext
// itself (no hardcoded magic numbers), and bounds are checked against the
// named package constants minHeadBytes / maxHeadBytes.
func TestComputeHeadBudget(t *testing.T) {
	tests := []struct {
		name        string
		nCtx        int
		expectFloor bool // budget should equal minHeadBytes
		expectCeil  bool // budget should equal maxHeadBytes
	}{
		{name: "default production 128k context — scales", nCtx: 131072},
		{name: "small 8k context — floored", nCtx: 8192, expectFloor: true},
		{name: "large 1M context — ceiling", nCtx: 1000000, expectCeil: true},
		{name: "zero context — fallback to 4096", nCtx: 0, expectFloor: true},
		{name: "tiny 100-token context", nCtx: 100, expectFloor: true},
		{name: "32k context — scales", nCtx: 32768},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Agent{cfg: &config.Config{LLM: config.LLMConfig{MaxContextTokens: tt.nCtx}}}
			b := computeHeadBudget(a)

			// computeHeadBudget must agree with the exported helper.
			expected := HeadBudgetForContext(tt.nCtx)
			if b != expected {
				t.Fatalf("computeHeadBudget(%d)=%d != HeadBudgetForContext(%d)=%d",
					tt.nCtx, b, tt.nCtx, expected)
			}

			// The budget must always be within the absolute bounds.
			if b < minHeadBytes {
				t.Fatalf("budget %d below floor %d", b, minHeadBytes)
			}
			if b > maxHeadBytes {
				t.Fatalf("budget %d exceeds ceiling %d", b, maxHeadBytes)
			}

			if tt.expectFloor && b != minHeadBytes {
				t.Fatalf("expected floor %d for nCtx=%d, got %d", minHeadBytes, tt.nCtx, b)
			}
			if tt.expectCeil && b != maxHeadBytes {
				t.Fatalf("expected ceiling %d for nCtx=%d, got %d", maxHeadBytes, tt.nCtx, b)
			}
		})
	}
}

// TestComputeHeadBudgetNilAgent ensures the function never panics when
// called with a nil Agent or nil config (defensive — happens in some
// init-order paths).
func TestComputeHeadBudgetNilAgent(t *testing.T) {
	b := computeHeadBudget(nil)
	if b < minHeadBytes {
		t.Fatalf("nil agent budget %d below floor %d", b, minHeadBytes)
	}
}

// newTestToolCache creates an in-memory toolcache.Cache backed by the
// production migration (store.Open) so the schema stays in sync with
// internal/store/db.go:migrate rather than duplicating CREATE TABLE
// statements that could silently drift.
func newTestToolCache(t *testing.T) *toolcache.Cache {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c, err := toolcache.New(toolcache.Config{
		DB: s.SQL(), CacheDir: t.TempDir(), TTLDefault: time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestSpilloverEngagesAtComputedHeadBudget verifies that a payload above the
// computed head budget spills into the cache, while a payload below the
// budget stays inline. The budget is obtained by calling HeadBudgetForContext
// (not a hardcoded number), and the payload sizes are built relative to it.
func TestSpilloverEngagesAtComputedHeadBudget(t *testing.T) {
	// Derive the budget from the same exported helper the production path
	// uses — no magic number, so the test tracks the formula automatically.
	budget := HeadBudgetForContext(131072)

	c := newTestToolCache(t)

	// Payload above the computed budget — must spill.
	aboveBudget := bytes.Repeat([]byte("x"), budget+1)
	if len(aboveBudget) <= budget {
		t.Fatalf("payload %d must exceed computed budget %d", len(aboveBudget), budget)
	}
	out, err := c.Put(context.Background(), toolcache.PutInput{
		User:        "alice",
		ConvID:      "c1",
		ToolName:    "builtin__web_fetch",
		Args:        map[string]any{"url": "https://example.com/article"},
		Payload:     aboveBudget,
		ContentType: "text/plain",
		Category:    "web_fetch",
		HeadBudget:  budget,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !out.Truncated {
		t.Error("payload above budget should spill (truncated=true)")
	}
	if len(out.Head) > budget {
		t.Errorf("head %d exceeds budget %d", len(out.Head), budget)
	}
	if out.TotalBytes != len(aboveBudget) {
		t.Errorf("TotalBytes = %d, want %d", out.TotalBytes, len(aboveBudget))
	}
	// Spillover path: a cache row must exist — More retrieves the tail.
	more, err := c.More(context.Background(), "alice", out.ID, len(out.Head), maxHeadBytes)
	if err != nil {
		t.Fatalf("cache row should exist for spilled payload: %v", err)
	}
	if len(more.Data) == 0 {
		t.Error("More should return tail data for spilled payload")
	}

	// Payload below the computed budget — must stay inline.
	belowBudget := bytes.Repeat([]byte("x"), budget-1)
	if len(belowBudget) >= budget {
		t.Fatalf("payload %d must be below computed budget %d", len(belowBudget), budget)
	}
	sout, err := c.Put(context.Background(), toolcache.PutInput{
		User:        "alice",
		ConvID:      "c1",
		ToolName:    "builtin__file_read",
		Args:        map[string]any{"path": "/docs/notes.txt"},
		Payload:     belowBudget,
		ContentType: "text/plain",
		HeadBudget:  budget,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if sout.Truncated {
		t.Error("payload below budget should not be truncated")
	}
	if !bytes.Equal(sout.Head, belowBudget) {
		t.Error("inline result head should equal full payload")
	}
	// Inline path: no cache row — More returns ErrNotFound.
	if _, err := c.More(context.Background(), "alice", sout.ID, 0, maxHeadBytes); err != toolcache.ErrNotFound {
		t.Errorf("expected ErrNotFound for inline put (no cache row), got %v", err)
	}
}
