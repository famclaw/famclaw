package agent

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/toolcache"
)

// TestComputeHeadBudget verifies the head-budget formula, its absolute
// floor/ceiling, and that the default production context (131072) yields a
// threshold in the realistic-web-fetch range.
func TestComputeHeadBudget(t *testing.T) {
	tests := []struct {
		name      string
		nCtx      int
		wantMin   int
		wantMax   int
		wantExact int // when set (>0), requires an exact match
	}{
		{
			name:      "default production 128k context — spill engages for realistic web fetches",
			nCtx:      131072,
			wantMin:   4096,  // > 4KB — large enough to be a useful preview
			wantMax:   16384, // < 16KB — small enough that a typical web page spills
			wantExact: 8708,
		},
		{
			name:      "small 8k context — floored to min preview",
			nCtx:      8192,
			wantMin:   512,
			wantMax:   512,
			wantExact: 512,
		},
		{
			name:      "large 1M context — ceiling caps preview at 64KB",
			nCtx:      1000000,
			wantMin:   65536,
			wantMax:   65536,
			wantExact: 65536,
		},
		{
			name:      "zero context falls back to 4096",
			nCtx:      0,
			wantMin:   512,
			wantMax:   512,
			wantExact: 512,
		},
		{
			name:      "tiny 100-token context — floored to min preview",
			nCtx:      100,
			wantMin:   512,
			wantMax:   512,
			wantExact: 512,
		},
		{
			name:      "32k context — scales proportionally",
			nCtx:      32768,
			wantMin:   1024,
			wantMax:   4096,
			wantExact: 2024,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Agent{cfg: &config.Config{LLM: config.LLMConfig{MaxContextTokens: tt.nCtx}}}
			b := computeHeadBudget(a)
			if b < tt.wantMin {
				t.Fatalf("budget %d below min %d", b, tt.wantMin)
			}
			if b > tt.wantMax {
				t.Fatalf("budget %d exceeds max %d", b, tt.wantMax)
			}
			if tt.wantExact > 0 && b != tt.wantExact {
				t.Fatalf("budget %d != exact %d", b, tt.wantExact)
			}
		})
	}
}

// TestComputeHeadBudgetNilAgent ensures the function never panics when
// called with a nil Agent or nil config (defensive — happens in some
// init-order paths).
func TestComputeHeadBudgetNilAgent(t *testing.T) {
	b := computeHeadBudget(nil)
	if b < 512 {
		t.Fatalf("nil agent budget %d below floor 512", b)
	}
}

// newTestToolCache creates an in-memory toolcache.Cache suitable for
// integration tests that exercise spillover with the real budget helper.
func newTestToolCache(t *testing.T) *toolcache.Cache {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for _, s := range []string{
		`CREATE TABLE tool_result_cache (
			id TEXT PRIMARY KEY, user_name TEXT, conv_id TEXT, tool_name TEXT,
			args_hash TEXT, payload_path TEXT, bytes INTEGER, content_type TEXT,
			created_at INTEGER, expires_at INTEGER, accessed_at INTEGER)`,
		`CREATE TABLE tool_result_audit (
			id TEXT PRIMARY KEY, user_name TEXT, conv_id TEXT, tool_name TEXT,
			args_hash TEXT, args_summary TEXT, bytes INTEGER, content_type TEXT,
			category TEXT, created_at INTEGER, payload_id TEXT, payload_purged_at INTEGER)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}
	c, err := toolcache.New(toolcache.Config{
		DB: db, CacheDir: t.TempDir(), TTLDefault: time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestSpilloverEngagesAtComputedHeadBudget verifies that a realistically-sized
// web page spills into the cache at the budget produced by the real
// HeadBudgetForContext helper (not a hardcoded magic number). It asserts the
// relationship that matters: a payload ABOVE the computed budget spills
// (cache row exists, head is truncated), while a payload BELOW the budget
// stays inline (no cache row).
func TestSpilloverEngagesAtComputedHeadBudget(t *testing.T) {
	// Derive the budget from the same exported helper the production path
	// uses — no magic number, so the test tracks the formula automatically.
	budget := HeadBudgetForContext(131072)
	if budget < 4096 || budget > 16384 {
		t.Fatalf("computed budget %d outside realistic range [4096, 16384]", budget)
	}

	c := newTestToolCache(t)

	// A realistic web fetch: ~15 KB of rendered page text. Larger than the
	// computed budget, so it must spill.
	webFetch := bytes.Repeat(
		[]byte("This is a paragraph of web page content that a real fetch would return. "),
		240,
	)
	if len(webFetch) <= budget {
		t.Fatalf("web fetch payload %d must exceed computed budget %d", len(webFetch), budget)
	}
	out, err := c.Put(context.Background(), toolcache.PutInput{
		User:        "alice",
		ConvID:      "c1",
		ToolName:    "builtin__web_fetch",
		Args:        map[string]any{"url": "https://example.com/article"},
		Payload:     webFetch,
		ContentType: "text/plain",
		Category:    "web_fetch",
		HeadBudget:  budget,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !out.Truncated {
		t.Error("web fetch above budget should spill (truncated=true)")
	}
	if len(out.Head) > budget {
		t.Errorf("head %d exceeds budget %d", len(out.Head), budget)
	}
	if out.TotalBytes != len(webFetch) {
		t.Errorf("TotalBytes = %d, want %d", out.TotalBytes, len(webFetch))
	}
	// Spillover path: a cache row must exist — More retrieves the tail.
	more, err := c.More(context.Background(), "alice", out.ID, len(out.Head), 8192)
	if err != nil {
		t.Fatalf("cache row should exist for spilled payload: %v", err)
	}
	if len(more.Data) == 0 {
		t.Error("More should return tail data for spilled payload")
	}

	// A small result (sub-budget): stays inline, no cache row.
	small := bytes.Repeat([]byte("Short answer. "), 140)
	if len(small) >= budget {
		t.Fatalf("small payload %d must be below budget %d", len(small), budget)
	}
	sout, err := c.Put(context.Background(), toolcache.PutInput{
		User:        "alice",
		ConvID:      "c1",
		ToolName:    "builtin__file_read",
		Args:        map[string]any{"path": "/docs/notes.txt"},
		Payload:     small,
		ContentType: "text/plain",
		HeadBudget:  budget,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if sout.Truncated {
		t.Error("small result should not be truncated")
	}
	if !bytes.Equal(sout.Head, small) {
		t.Error("inline result head should equal full payload")
	}
	// Inline path: no cache row — More returns ErrNotFound.
	if _, err := c.More(context.Background(), "alice", sout.ID, 0, 8192); err != toolcache.ErrNotFound {
		t.Errorf("expected ErrNotFound for inline put (no cache row), got %v", err)
	}
}
