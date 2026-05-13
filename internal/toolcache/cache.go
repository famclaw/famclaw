package toolcache

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Config configures a Cache instance.
type Config struct {
	DB         *sql.DB                  // open sqlite handle (modernc.org/sqlite). Required.
	CacheDir   string                   // root dir for payload files. Empty = DefaultCacheDir().
	TTLDefault time.Duration            // fallback TTL when no role-specific override applies. Default 6h.
	TTLByRole  map[string]time.Duration // role → TTL override (parent, age_13_17, age_8_12, under_8)
	PerUserCap int64                    // bytes; 0 disables per-user LRU eviction.
}

// Cache is the public spillover-cache API.
type Cache struct {
	cfg   Config
	store *store
	idgen *IDGenerator

	sweepMu   sync.Mutex
	sweepStop chan struct{}
}

// New initializes the cache: ensures cache_dir exists, sets defaults, builds
// the store. Does NOT start the sweeper — call StartSweeper explicitly so
// tests can run Sweep deterministically. Called once at process boot; uses
// context.Background for the one-shot DefaultCacheDir lookup.
func New(cfg Config) (*Cache, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("toolcache.New: DB is required")
	}
	if cfg.CacheDir == "" {
		dir, err := DefaultCacheDir(context.Background())
		if err != nil {
			return nil, fmt.Errorf("toolcache.New: default dir: %w", err)
		}
		cfg.CacheDir = dir
	}
	if err := os.MkdirAll(cfg.CacheDir, 0700); err != nil {
		return nil, fmt.Errorf("toolcache.New: mkdir %s: %w", cfg.CacheDir, err)
	}
	if cfg.TTLDefault == 0 {
		cfg.TTLDefault = 6 * time.Hour
	}
	return &Cache{
		cfg:   cfg,
		store: &store{db: cfg.DB},
		idgen: NewIDGenerator(),
	}, nil
}

// PutInput is the input to Cache.Put.
type PutInput struct {
	User        string         // identity-resolved user name
	UserRole    string         // optional, drives per-role TTL selection
	ConvID      string         // conversation id
	ToolName    string         // "builtin__web_fetch", etc.
	Args        map[string]any // canonicalized for dedup
	Payload     []byte         // raw bytes
	ContentType string         // "text/plain", "image/png", etc.
	Category    string         // optional, audit only
	HeadBudget  int            // max bytes the head slice can occupy
}

// PutOutput is returned from Cache.Put.
type PutOutput struct {
	ID         string // ULID identifier for this stored result
	Head       []byte // head slice of payload, budget-sized, sentence-boundary
	Truncated  bool   // true if Head is shorter than full Payload
	TotalBytes int    // full payload size (so the model knows what to ask for)
	Deduped    bool   // true if this Put hit an existing row (no new file written)
}

// Put stores payload + audit row, returns id + head slice. Small payloads
// that fit within HeadBudget bypass the cache (inline path) — only an audit
// row is written. Dedup: if (user, tool, canonical args) exists and is not
// yet expired, refresh accessed_at + expires_at and return the existing id
// with a fresh head from the cached payload.
func (c *Cache) Put(ctx context.Context, in PutInput) (PutOutput, error) {
	now := time.Now().UnixMilli()
	argsHash, err := CanonicalArgsHash(in.Args)
	if err != nil {
		return PutOutput{}, fmt.Errorf("toolcache.Put hash args: %w", err)
	}
	ttl := c.ttlFor(in.UserRole)

	// Dedup
	if existing, derr := c.store.findDedup(ctx, in.User, in.ToolName, argsHash, now); derr == nil {
		_ = c.store.touchAccessed(ctx, existing.ID, now)
		_ = c.store.refreshExpiry(ctx, existing.ID, now+ttl.Milliseconds())
		full, rerr := readPayload(c.cfg.CacheDir, existing.PayloadPath, 0, int(existing.Bytes))
		if rerr != nil {
			// Row exists but file is missing — proceed to a fresh write below.
			_ = c.store.deleteCache(ctx, existing.ID)
		} else {
			head, truncated := buildHead(full, in.HeadBudget)
			return PutOutput{
				ID: existing.ID, Head: head, Truncated: truncated,
				TotalBytes: int(existing.Bytes), Deduped: true,
			}, nil
		}
	}

	// Inline path: payload fits within budget. No file, no cache row — only
	// an audit row for parent oversight. Returns the payload unchanged with
	// a freshly minted id so callers can reference it consistently.
	id, err := c.idgen.Generate()
	if err != nil {
		return PutOutput{}, fmt.Errorf("toolcache.Put mint id: %w", err)
	}
	if in.HeadBudget > 0 && len(in.Payload) <= in.HeadBudget {
		audit := auditRow{
			ID: id, UserName: in.User, ConvID: in.ConvID, ToolName: in.ToolName,
			ArgsHash: argsHash, ArgsSummary: summarizeArgs(in.Args),
			Bytes: int64(len(in.Payload)), ContentType: in.ContentType,
			CreatedAt: now,
		}
		if in.Category != "" {
			audit.Category = sql.NullString{String: in.Category, Valid: true}
		}
		if aerr := c.store.insertAudit(ctx, audit); aerr != nil {
			log.Printf("[toolcache] audit insert failed (non-fatal): %v", aerr)
		}
		return PutOutput{
			ID: id, Head: in.Payload, Truncated: false,
			TotalBytes: len(in.Payload), Deduped: false,
		}, nil
	}

	// Spillover path: write file, then cache row, then audit. If the row
	// insert fails, delete the file to avoid orphaning.
	rel, werr := writePayload(ctx, c.cfg.CacheDir, in.User, id, in.Payload)
	if werr != nil {
		return PutOutput{}, werr
	}
	row := cacheRow{
		ID: id, UserName: in.User, ConvID: in.ConvID, ToolName: in.ToolName,
		ArgsHash: argsHash, PayloadPath: rel, Bytes: int64(len(in.Payload)),
		ContentType: in.ContentType, CreatedAt: now,
		ExpiresAt: now + ttl.Milliseconds(), AccessedAt: now,
	}
	if ierr := c.store.insertCache(ctx, row); ierr != nil {
		_ = deletePayload(c.cfg.CacheDir, rel)
		return PutOutput{}, ierr
	}
	auditID, aierr := c.idgen.Generate()
	if aierr != nil {
		// Audit ID failure is non-fatal — the cache row + payload are already
		// written. Log and skip the audit row; the cache remains consistent.
		log.Printf("[toolcache] audit id mint failed (non-fatal): %v", aierr)
	} else {
		audit := auditRow{
			ID: auditID, UserName: in.User, ConvID: in.ConvID, ToolName: in.ToolName,
			ArgsHash: argsHash, ArgsSummary: summarizeArgs(in.Args),
			Bytes: int64(len(in.Payload)), ContentType: in.ContentType,
			CreatedAt: now,
			PayloadID: sql.NullString{String: id, Valid: true},
		}
		if in.Category != "" {
			audit.Category = sql.NullString{String: in.Category, Valid: true}
		}
		if aerr := c.store.insertAudit(ctx, audit); aerr != nil {
			log.Printf("[toolcache] audit insert failed (non-fatal): %v", aerr)
		}
	}

	head, truncated := buildHead(in.Payload, in.HeadBudget)
	log.Printf("[toolcache] put user=%s tool=%s bytes=%d head=%d id=%s",
		in.User, in.ToolName, len(in.Payload), len(head), id)
	return PutOutput{
		ID: id, Head: head, Truncated: truncated,
		TotalBytes: len(in.Payload), Deduped: false,
	}, nil
}

// MoreOutput is returned from Cache.More.
type MoreOutput struct {
	Data        []byte
	ContentType string
	TotalBytes  int
	Offset      int // echo of requested offset (after clamping)
	Length      int // actual bytes returned (may be less if EOF)
}

// More reads (offset, length) bytes from a spilled payload. Enforces
// per-user ownership — cross-user access returns ErrNotFound (not
// ErrForbidden) to avoid leaking cross-user row existence. Length is
// clamped to [1, 8192]. Updates accessed_at on success.
func (c *Cache) More(ctx context.Context, user, id string, offset, length int) (MoreOutput, error) {
	row, err := c.store.getCacheByID(ctx, user, id)
	if err != nil {
		return MoreOutput{}, err
	}
	if offset < 0 {
		offset = 0
	}
	if offset > int(row.Bytes) {
		offset = int(row.Bytes)
	}
	if length < 1 {
		length = 4096
	}
	if length > 8192 {
		length = 8192
	}
	data, rerr := readPayload(c.cfg.CacheDir, row.PayloadPath, offset, length)
	if rerr != nil {
		// File gone — clean up the row and surface NotFound.
		_ = c.store.deleteCache(ctx, id)
		return MoreOutput{}, ErrNotFound
	}
	now := time.Now().UnixMilli()
	_ = c.store.touchAccessed(ctx, id, now)
	log.Printf("[toolcache] more user=%s id=%s offset=%d returned=%d", user, id, offset, len(data))
	return MoreOutput{
		Data: data, ContentType: row.ContentType, TotalBytes: int(row.Bytes),
		Offset: offset, Length: len(data),
	}, nil
}

// SweepResult counts what Sweep removed.
type SweepResult struct {
	TTLDeleted int
	LRUEvicted int
	FreedBytes int64
}

// Sweep runs TTL purge + per-user LRU cap enforcement once. See spec §3.
func (c *Cache) Sweep(ctx context.Context) (SweepResult, error) {
	now := time.Now().UnixMilli()
	var res SweepResult

	// Pass 1: TTL
	expired, err := c.store.listExpired(ctx, now)
	if err != nil {
		return res, err
	}
	for _, r := range expired {
		_ = deletePayload(c.cfg.CacheDir, r.PayloadPath)
		_ = c.store.deleteCache(ctx, r.ID)
		_ = c.store.markAuditPayloadPurged(ctx, r.ID, now)
		res.TTLDeleted++
		res.FreedBytes += r.Bytes
	}

	// Pass 2: per-user LRU cap
	if c.cfg.PerUserCap > 0 {
		users, uerr := c.store.distinctUsers(ctx)
		if uerr == nil {
			for _, user := range users {
				rows, err := c.store.listByUserOrderByAccessed(ctx, user)
				if err != nil {
					continue
				}
				var total int64
				for _, r := range rows {
					total += r.Bytes
				}
				for _, r := range rows {
					if total <= c.cfg.PerUserCap {
						break
					}
					_ = deletePayload(c.cfg.CacheDir, r.PayloadPath)
					_ = c.store.deleteCache(ctx, r.ID)
					_ = c.store.markAuditPayloadPurged(ctx, r.ID, now)
					res.LRUEvicted++
					res.FreedBytes += r.Bytes
					total -= r.Bytes
				}
			}
		}
	}

	log.Printf("[toolcache] sweep ttl_deleted=%d lru_evicted=%d freed_bytes=%d",
		res.TTLDeleted, res.LRUEvicted, res.FreedBytes)
	return res, nil
}

// Reconcile walks the cache dir and reconciles against the DB. Deletes
// orphan files (no row) and orphan rows (no file). Idempotent. Called
// once at boot to recover from crashes between file write and row insert
// (or vice versa).
func (c *Cache) Reconcile(ctx context.Context) error {
	start := time.Now()
	var orphanFiles, orphanRows int

	// Pass 1: walk filesystem, check each file against the DB.
	if entries, err := os.ReadDir(c.cfg.CacheDir); err == nil {
		for _, userEntry := range entries {
			if !userEntry.IsDir() {
				continue
			}
			user := userEntry.Name()
			userDir := filepath.Join(c.cfg.CacheDir, user)
			files, err := os.ReadDir(userDir)
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() {
					continue
				}
				name := f.Name()
				if !strings.HasSuffix(name, ".bin") {
					continue
				}
				id := strings.TrimSuffix(name, ".bin")
				if _, err := c.store.getCacheByID(ctx, user, id); err == ErrNotFound {
					_ = os.Remove(filepath.Join(userDir, name))
					orphanFiles++
				}
			}
		}
	}

	// Pass 2: walk DB, check each row's file. Collect orphan IDs first;
	// deleting during iteration can deadlock on the rows cursor.
	type orphan struct {
		id  string
		rel string
	}
	var orphans []orphan
	rows, err := c.cfg.DB.QueryContext(ctx, `SELECT id, payload_path FROM tool_result_cache`)
	if err == nil {
		for rows.Next() {
			var id, rel string
			if err := rows.Scan(&id, &rel); err != nil {
				continue
			}
			if _, err := statPayload(c.cfg.CacheDir, rel); err != nil {
				orphans = append(orphans, orphan{id: id, rel: rel})
			}
		}
		rows.Close()
	}
	for _, o := range orphans {
		_ = c.store.deleteCache(ctx, o.id)
		orphanRows++
	}

	elapsed := time.Since(start)
	log.Printf("[toolcache] reconcile orphan_files=%d orphan_rows=%d elapsed=%s",
		orphanFiles, orphanRows, elapsed)
	if elapsed > 5*time.Second {
		log.Printf("[toolcache] reconcile slow (>5s); cache may be unusually large")
	}
	return nil
}

// ttlFor returns the TTL for a given user role. Falls back to TTLDefault
// when no role-specific override exists.
func (c *Cache) ttlFor(role string) time.Duration {
	if role != "" && c.cfg.TTLByRole != nil {
		if d, ok := c.cfg.TTLByRole[role]; ok {
			return d
		}
	}
	return c.cfg.TTLDefault
}

// buildHead returns a budget-sized prefix of payload and a Truncated flag.
// Sentence-boundary aware: looks back up to 200 bytes from the budget for
// a sentence terminator (., ?, !, \n) to avoid cutting mid-sentence.
func buildHead(payload []byte, budget int) ([]byte, bool) {
	if budget <= 0 || len(payload) <= budget {
		return payload, len(payload) > budget
	}
	end := budget
	lookback := budget - 200
	if lookback < 0 {
		lookback = 0
	}
	for i := budget - 1; i >= lookback; i-- {
		c := payload[i]
		if c == '.' || c == '!' || c == '?' || c == '\n' {
			end = i + 1
			break
		}
	}
	return payload[:end], true
}

// summarizeArgs renders args as a short audit-only preview. Prefers the
// "url" key when present (the typical case for web_fetch); otherwise
// emits the canonical JSON, truncated to 200 chars.
func summarizeArgs(args map[string]any) string {
	if u, ok := args["url"].(string); ok && u != "" {
		return "url=" + u
	}
	b, err := canonicalizeArgs(args)
	if err != nil {
		return ""
	}
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}
