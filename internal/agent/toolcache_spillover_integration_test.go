package agent

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/toolcache"
	"github.com/famclaw/famclaw/internal/webfetch"
)

// prodContextTokens is the MaxContextTokens used by the captain's live
// homelab config. The test derives the spillover threshold from it rather
// than from a hardcoded budget number, so a change to the context-window
// size or the budget formula automatically scales the test payload.
const prodContextTokens = 131072

// TestWebFetchSpilloverIntegration verifies the tool-result spillover path
// end-to-end: when web_fetch returns a payload larger than the head budget
// (the spillover threshold), the agent writes a tool_result_cache row
// linked from a tool_result_audit row (via payload_id) and the full payload
// — including the bytes beyond the inline head — is retrievable through
// Cache.More.
//
// This is the integration test audit finding 5 (internal/toolcache "never
// fired") asked for. In production every web_fetch result was ≤70KB, well
// under the ~8.5KB threshold (with headShare=0.02), so the cache stayed
// empty. This test forces a payload strictly above the threshold to prove
// the spillover path actually works rather than merely existing.
func TestWebFetchSpilloverIntegration(t *testing.T) {
	// Derive the spillover threshold from the exported helper rather than
	// hardcoding a budget number. With headShare=0.02 at 131072 tokens the
	// budget is ~8.5 KB, so the threshold scales automatically if the
	// context size or formula changes.
	headBudget := HeadBudgetForContext(prodContextTokens)
	if headBudget <= 0 {
		t.Fatalf("HeadBudgetForContext returned non-positive %d", headBudget)
	}
	t.Logf("head budget threshold = %d bytes (%.1f KB); spillover fires for payloads > this",
		headBudget, float64(headBudget)/1024.0)

	// Build a payload strictly larger than the head budget. The content is a
	// deterministic A-Z pattern (no sentence terminators) so buildHead falls
	// back to an exact-budget head length, making the tail offset predictable.
	const tailExtra = 8192
	payload := make([]byte, headBudget+tailExtra)
	for i := range payload {
		payload[i] = 'A' + byte(i%26)
	}
	body := string(payload)

	// In-memory sqlite with the two toolcache tables (same schema as
	// internal/store/db.go:migrate).
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
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
			t.Fatalf("schema: %v", err)
		}
	}

	cacheDir := t.TempDir()
	c, err := toolcache.New(toolcache.Config{
		DB: db, CacheDir: cacheDir, TTLDefault: time.Hour,
	})
	if err != nil {
		t.Fatalf("toolcache.New: %v", err)
	}
	// New() does not start the sweeper (see toolcache.New doc), so there is
	// no background goroutine to stop. StopSweeper is idempotent and a no-op
	// here — called for defensiveness so the cache is never leaked if
	// StartSweeper is ever called in this test.
	t.Cleanup(func() { c.StopSweeper() })

	a := &Agent{
		user: &config.UserConfig{Name: "spillover-test", Role: "parent"},
		cfg: &config.Config{
			LLM: config.LLMConfig{MaxContextTokens: prodContextTokens},
			Tools: config.ToolsConfig{
				WebFetch: config.WebFetchConfig{
					Enabled:      true,
					URLAllowlist: []string{"example.com"},
					MaxBytes:     256 * 1024,
					TimeoutSec:   5,
				},
			},
		},
		webFetcher: func(_ context.Context, _ string, _ webfetch.Options) (*webfetch.Result, error) {
			return &webfetch.Result{
				URL:         "https://example.com/big",
				StatusCode:  200,
				ContentType: "text/plain",
				Bytes:       int64(len(body)),
				Truncated:   false,
				Text:        body,
			}, nil
		},
		cache:  c,
		convID: "spillover-conv",
	}

	out, err := a.handleWebFetch(context.Background(), map[string]any{
		"url": "https://example.com/big",
	})
	if err != nil {
		t.Fatalf("handleWebFetch: %v", err)
	}

	// The response must NOT be the full inline payload — it must be a
	// budget-sized head plus a truncation marker and a tool_result_more hint.
	if strings.Contains(out, body) {
		t.Errorf("output contained the full payload inline; spillover did not engage")
	}
	if !strings.Contains(out, "[truncated") {
		t.Errorf("expected output to contain truncation marker, got:\n%s", out)
	}
	if !strings.Contains(out, "Cache id:") {
		t.Errorf("expected output to contain 'Cache id:', got:\n%s", out)
	}
	if !strings.Contains(out, "tool_result_more") {
		t.Errorf("expected output to contain tool_result_more hint, got:\n%s", out)
	}

	// Resolve the cache id from the DB (not by parsing the output) so the
	// assertion is robust to header-format changes.
	var cacheID string
	if err := db.QueryRow(
		`SELECT id FROM tool_result_cache WHERE user_name = ? AND tool_name = ?`,
		"spillover-test", "builtin__web_fetch",
	).Scan(&cacheID); err != nil {
		t.Fatalf("expected tool_result_cache row: %v", err)
	}
	if cacheID == "" {
		t.Fatal("cache id is empty")
	}
	t.Logf("cache id = %s", cacheID)

	// The audit row must link to the cache row via payload_id (non-NULL).
	var payloadID sql.NullString
	var auditCount int
	if err := db.QueryRow(
		`SELECT payload_id, COUNT(*) FROM tool_result_audit WHERE payload_id = ?`, cacheID,
	).Scan(&payloadID, &auditCount); err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("expected 1 tool_result_audit row with payload_id=%s, got %d", cacheID, auditCount)
	}
	if !payloadID.Valid || payloadID.String != cacheID {
		t.Errorf("audit payload_id = valid=%v string=%q, want %s",
			payloadID.Valid, payloadID.String, cacheID)
	}

	// Verify the payload_path column points at a real file on disk whose
	// full contents match the original payload (not just the head slice).
	var payloadPath string
	var storedBytes int64
	if err := db.QueryRow(
		`SELECT payload_path, bytes FROM tool_result_cache WHERE id = ?`, cacheID,
	).Scan(&payloadPath, &storedBytes); err != nil {
		t.Fatalf("query payload_path: %v", err)
	}
	if storedBytes != int64(len(payload)) {
		t.Errorf("stored bytes = %d, want %d", storedBytes, len(payload))
	}
	onDisk, err := os.ReadFile(filepath.Join(cacheDir, payloadPath))
	if err != nil {
		t.Fatalf("read payload file %s: %v", payloadPath, err)
	}
	if !bytes.Equal(onDisk, payload) {
		t.Errorf("on-disk payload (%d bytes) does not match original (%d bytes)", len(onDisk), len(payload))
	}

	// Read the tail (offset = headBudget, past the inline head) back from the
	// cache and confirm it matches the original payload bytes. This proves
	// the *full* payload was spilled, not just the inline head. Clamp to the
	// actual bytes returned in case of a short read near EOF.
	tail, err := c.More(context.Background(), "spillover-test", cacheID, headBudget, 8192)
	if err != nil {
		t.Fatalf("Cache.More tail: %v", err)
	}
	if len(tail.Data) == 0 {
		t.Fatal("Cache.More tail returned 0 bytes")
	}
	wantLen := min(len(tail.Data), len(payload)-headBudget)
	wantTail := payload[headBudget : headBudget+wantLen]
	if !bytes.Equal(tail.Data, wantTail) {
		t.Errorf("tail mismatch: got %d bytes, want %d (got=%q want=%q)",
			len(tail.Data), wantLen,
			tail.Data[:min(16, len(tail.Data))], wantTail[:min(16, wantLen)])
	}

	// Read the head (offset 0) and confirm it is the budget-sized prefix.
	head, err := c.More(context.Background(), "spillover-test", cacheID, 0, 8192)
	if err != nil {
		t.Fatalf("Cache.More head: %v", err)
	}
	if len(head.Data) != 8192 {
		t.Errorf("head read returned %d bytes, want 8192", len(head.Data))
	}
}
