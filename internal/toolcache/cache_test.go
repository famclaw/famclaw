package toolcache

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newTestDB creates an in-memory sqlite with the two Phase 2 tables.
// Shared by store_test.go and cache_test.go.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	stmts := []string{
		`CREATE TABLE tool_result_cache (
			id TEXT PRIMARY KEY, user_name TEXT, conv_id TEXT, tool_name TEXT,
			args_hash TEXT, payload_path TEXT, bytes INTEGER, content_type TEXT,
			created_at INTEGER, expires_at INTEGER, accessed_at INTEGER)`,
		`CREATE TABLE tool_result_audit (
			id TEXT PRIMARY KEY, user_name TEXT, conv_id TEXT, tool_name TEXT,
			args_hash TEXT, args_summary TEXT, bytes INTEGER, content_type TEXT,
			category TEXT, created_at INTEGER, payload_id TEXT, payload_purged_at INTEGER)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %s: %v", s, err)
		}
	}
	return db
}

func newTestCache(t *testing.T) *Cache {
	t.Helper()
	db := newTestDB(t)
	root := t.TempDir()
	c, err := New(Config{DB: db, CacheDir: root, TTLDefault: time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestPutSmallPayloadReturnsInlineNoCacheRow(t *testing.T) {
	c := newTestCache(t)
	out, err := c.Put(context.Background(), PutInput{
		User: "alice", ConvID: "c1", ToolName: "tool",
		Args:    map[string]any{"k": "v"},
		Payload: []byte("small"), ContentType: "text/plain",
		HeadBudget: 100,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if out.Truncated {
		t.Error("small payload should not be truncated")
	}
	if !bytes.Equal(out.Head, []byte("small")) {
		t.Errorf("head = %q, want 'small'", out.Head)
	}
	if out.ID == "" {
		t.Error("ID should be assigned even for inline path")
	}
	// Inline path: no cache row should exist (only audit).
	if _, err := c.store.getCacheByID(context.Background(), "alice", out.ID); err != ErrNotFound {
		t.Errorf("expected no cache row for inline put, got err=%v", err)
	}
}

func TestPutLargePayloadTruncatesAndCaches(t *testing.T) {
	c := newTestCache(t)
	payload := make([]byte, 5000)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}
	out, err := c.Put(context.Background(), PutInput{
		User: "alice", ConvID: "c1", ToolName: "tool",
		Args: map[string]any{}, Payload: payload, ContentType: "text/plain",
		HeadBudget: 1000,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !out.Truncated {
		t.Error("large payload should be truncated")
	}
	if len(out.Head) > 1000 {
		t.Errorf("head exceeds budget: %d > 1000", len(out.Head))
	}
	if out.TotalBytes != 5000 {
		t.Errorf("TotalBytes = %d, want 5000", out.TotalBytes)
	}
	// Cache row should exist.
	row, err := c.store.getCacheByID(context.Background(), "alice", out.ID)
	if err != nil {
		t.Fatalf("cache row missing: %v", err)
	}
	if row.Bytes != 5000 {
		t.Errorf("row.Bytes = %d, want 5000", row.Bytes)
	}
}

// TestPutRealisticWebFetchSpills verifies that a realistically-sized web
// page — the kind builtin__web_fetch actually returns — spills into the
// cache under the production head budget. The head budget at the default
// 128K-token context is 8708 bytes (see agent.computeHeadBudget); a typical
// rendered web page is 10–30 KB, so a 15 KB payload must spill while a
// sub-threshold result stays inline. This is the case that was missing from
// PR 336, which used an artificially huge payload and thus never reflected
// the real production threshold.
func TestPutRealisticWebFetchSpills(t *testing.T) {
	// HeadBudget matches computeHeadBudget at 131072 tokens (the live
	// default context). See internal/agent/budget_test.go.
	const headBudget = 8708

	// 15 KB of plausible web-page body text — larger than the head budget,
	// so it must spill.
	payload := bytes.Repeat([]byte("This is a paragraph of web page content that a real fetch would return. "), 240)
	if len(payload) <= headBudget {
		t.Fatalf("test payload %d must exceed head budget %d", len(payload), headBudget)
	}

	c := newTestCache(t)
	out, err := c.Put(context.Background(), PutInput{
		User:        "alice",
		ConvID:      "c1",
		ToolName:    "builtin__web_fetch",
		Args:        map[string]any{"url": "https://example.com/article"},
		Payload:     payload,
		ContentType: "text/plain",
		Category:    "web_fetch",
		HeadBudget:  headBudget,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !out.Truncated {
		t.Error("15 KB web fetch should spill (truncated=true)")
	}
	if len(out.Head) > headBudget {
		t.Errorf("head %d exceeds budget %d", len(out.Head), headBudget)
	}
	if out.TotalBytes != len(payload) {
		t.Errorf("TotalBytes = %d, want %d", out.TotalBytes, len(payload))
	}
	// Spillover path: a cache row must exist.
	if _, err := c.store.getCacheByID(context.Background(), "alice", out.ID); err != nil {
		t.Fatalf("cache row should exist for spilled payload: %v", err)
	}
}

// TestPutRealisticSmallResultStaysInline verifies that a small tool result —
// the kind that should stay inline — does NOT spill. This is the
// counterpart to TestPutRealisticWebFetchSpills: normal replies stay inline
// while large web fetches spill.
func TestPutRealisticSmallResultStaysInline(t *testing.T) {
	const headBudget = 8708

	// ~2 KB of text — a small answer or short file read. Below the head
	// budget, so it must stay inline (no cache row).
	payload := bytes.Repeat([]byte("Short answer. "), 140)
	if len(payload) >= headBudget {
		t.Fatalf("test payload %d must be below head budget %d", len(payload), headBudget)
	}

	c := newTestCache(t)
	out, err := c.Put(context.Background(), PutInput{
		User:        "alice",
		ConvID:      "c1",
		ToolName:    "builtin__file_read",
		Args:        map[string]any{"path": "/docs/notes.txt"},
		Payload:     payload,
		ContentType: "text/plain",
		HeadBudget:  headBudget,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if out.Truncated {
		t.Error("small result should not be truncated")
	}
	if !bytes.Equal(out.Head, payload) {
		t.Error("inline result head should equal full payload")
	}
	// Inline path: no cache row, only an audit row.
	if _, err := c.store.getCacheByID(context.Background(), "alice", out.ID); err != ErrNotFound {
		t.Errorf("expected no cache row for inline put, got err=%v", err)
	}
}

func TestMoreReadsTailAfterHead(t *testing.T) {
	c := newTestCache(t)
	payload := make([]byte, 5000)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}
	out, _ := c.Put(context.Background(), PutInput{
		User: "alice", ToolName: "t", Args: map[string]any{}, Payload: payload,
		ContentType: "text/plain", HeadBudget: 1000,
	})
	more, err := c.More(context.Background(), "alice", out.ID, len(out.Head), 1000)
	if err != nil {
		t.Fatalf("More: %v", err)
	}
	if len(more.Data) == 0 {
		t.Error("More returned empty data")
	}
	if more.TotalBytes != 5000 {
		t.Errorf("more.TotalBytes = %d, want 5000", more.TotalBytes)
	}
}

func TestPutDedupSameArgsWithinTTL(t *testing.T) {
	c := newTestCache(t)
	args := map[string]any{"url": "https://x"}
	payload := make([]byte, 3000)
	out1, _ := c.Put(context.Background(), PutInput{
		User: "alice", ToolName: "fetch", Args: args, Payload: payload,
		ContentType: "text/plain", HeadBudget: 100,
	})
	out2, err := c.Put(context.Background(), PutInput{
		User: "alice", ToolName: "fetch", Args: args, Payload: payload,
		ContentType: "text/plain", HeadBudget: 100,
	})
	if err != nil {
		t.Fatalf("Put dup: %v", err)
	}
	if !out2.Deduped {
		t.Error("second Put with same args should be Deduped")
	}
	if out2.ID != out1.ID {
		t.Errorf("dedup should return same ID: %q vs %q", out1.ID, out2.ID)
	}
}

func TestMoreCrossUserReturnsNotFound(t *testing.T) {
	c := newTestCache(t)
	payload := make([]byte, 5000)
	out, _ := c.Put(context.Background(), PutInput{
		User: "alice", ToolName: "t", Args: map[string]any{},
		Payload: payload, ContentType: "text/plain", HeadBudget: 100,
	})
	_, err := c.More(context.Background(), "bob", out.ID, 0, 100)
	if err != ErrNotFound {
		t.Errorf("cross-user More should return ErrNotFound, got %v", err)
	}
}

func TestSweepRemovesExpired(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	c, _ := New(Config{DB: db, CacheDir: root, TTLDefault: -time.Minute})
	// Negative TTL = already expired; Sweep should reap immediately.
	payload := make([]byte, 3000)
	_, _ = c.Put(context.Background(), PutInput{
		User: "alice", ToolName: "t", Args: map[string]any{},
		Payload: payload, ContentType: "text/plain", HeadBudget: 100,
	})
	res, err := c.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.TTLDeleted < 1 {
		t.Errorf("expected ≥1 TTL deletion, got %d", res.TTLDeleted)
	}
	if res.FreedBytes != 3000 {
		t.Errorf("FreedBytes = %d, want 3000", res.FreedBytes)
	}
}

func TestSweepLRUEvictsOverPerUserCap(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	c, _ := New(Config{
		DB: db, CacheDir: root,
		TTLDefault: time.Hour, PerUserCap: 4000, // bytes
	})
	// Write 3 payloads, each 2KB. Total 6KB, cap 4KB → LRU evicts oldest.
	for i := 0; i < 3; i++ {
		_, _ = c.Put(context.Background(), PutInput{
			User: "alice", ToolName: "t",
			Args:    map[string]any{"i": i},
			Payload: make([]byte, 2000), ContentType: "text/plain",
			HeadBudget: 100,
		})
		time.Sleep(2 * time.Millisecond) // accessed_at ordering
	}
	res, err := c.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.LRUEvicted < 1 {
		t.Errorf("expected ≥1 LRU eviction, got %d", res.LRUEvicted)
	}
}

func TestReconcileDeletesOrphanFiles(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	c, _ := New(Config{DB: db, CacheDir: root, TTLDefault: time.Hour})
	// Create a file with no corresponding row.
	orphanDir := filepath.Join(root, "alice")
	_ = os.MkdirAll(orphanDir, 0700)
	orphanFile := filepath.Join(orphanDir, "01ORPHAN.bin")
	_ = os.WriteFile(orphanFile, []byte("x"), 0600)

	if err := c.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := os.Stat(orphanFile); !os.IsNotExist(err) {
		t.Errorf("orphan file should have been deleted, stat err=%v", err)
	}
}

func TestReconcileDeletesOrphanRows(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	c, _ := New(Config{DB: db, CacheDir: root, TTLDefault: time.Hour})
	// Insert a row whose file doesn't exist.
	now := time.Now().UnixMilli()
	_ = c.store.insertCache(context.Background(), cacheRow{
		ID: "01PHANTOM", UserName: "alice", ConvID: "c", ToolName: "t",
		ArgsHash: "x", PayloadPath: "alice/01PHANTOM.bin", Bytes: 100,
		ContentType: "text/plain",
		CreatedAt:   now, ExpiresAt: now + 3600000, AccessedAt: now,
	})
	if err := c.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := c.store.getCacheByID(context.Background(), "alice", "01PHANTOM"); err != ErrNotFound {
		t.Errorf("phantom row should be deleted, err=%v", err)
	}
}

func TestTTLByRoleOverridesDefault(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	c, _ := New(Config{
		DB: db, CacheDir: root, TTLDefault: 24 * time.Hour,
		TTLByRole: map[string]time.Duration{"age_13_17": 6 * time.Hour},
	})
	if got := c.ttlFor("age_13_17"); got != 6*time.Hour {
		t.Errorf("teen TTL = %v, want 6h", got)
	}
	if got := c.ttlFor("parent"); got != 24*time.Hour {
		t.Errorf("parent (no override) = %v, want default 24h", got)
	}
	if got := c.ttlFor(""); got != 24*time.Hour {
		t.Errorf("empty role = %v, want default 24h", got)
	}
}

func TestBuildHeadSentenceBoundary(t *testing.T) {
	payload := []byte("First sentence. Second sentence. Third sentence is much longer than the others.")
	head, truncated := buildHead(payload, 35)
	if !truncated {
		t.Error("expected truncated")
	}
	// Should end at the period after "Second sentence."
	if !bytes.HasSuffix(head, []byte("sentence.")) {
		t.Errorf("head should end at sentence boundary, got %q", head)
	}
}

func TestBuildHeadNoBoundaryFallsBack(t *testing.T) {
	payload := []byte("nopunctuationhereatallnopunctuationhereatall")
	head, truncated := buildHead(payload, 20)
	if !truncated {
		t.Error("expected truncated")
	}
	if len(head) != 20 {
		t.Errorf("no-boundary fallback should be exact budget length, got %d", len(head))
	}
}

func TestSummarizeArgsPrefersURL(t *testing.T) {
	got := summarizeArgs(map[string]any{"url": "https://x.com", "other": "stuff"})
	if got != "url=https://x.com" {
		t.Errorf("summarizeArgs = %q, want 'url=https://x.com'", got)
	}
}

func TestSummarizeArgsFallback(t *testing.T) {
	got := summarizeArgs(map[string]any{"a": 1, "b": 2})
	if got != `{"a":1,"b":2}` {
		t.Errorf("summarizeArgs fallback = %q", got)
	}
}
