// Package store provides a SQLite-backed persistent store for FamClaw.
// Uses modernc.org/sqlite — pure Go, no CGO, cross-compiles to arm/arm64/android.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrApprovalAlreadyDecided is returned by DecideApprovalWithNote when the
// target approval is missing OR is no longer in `pending` state. Distinguishing
// this from a generic error lets callers surface "already decided" instead of
// "not found" and prevents two parents from racing to overwrite each other's
// decisions.
var ErrApprovalAlreadyDecided = errors.New("approval not found or already decided")

// DB is the FamClaw database.
type DB struct {
	sql *sql.DB
}

// Open opens (or creates) the SQLite database at the given path.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("creating db dir: %w", err)
	}

	db, err := sql.Open("sqlite", path+"?_journal=WAL&_timeout=5000&_fk=true")
	if err != nil {
		return nil, fmt.Errorf("opening sqlite: %w", err)
	}

	// Single writer, multiple readers — optimal for RPi
	db.SetMaxOpenConns(1)

	s := &DB{sql: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migration: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.sql.Close() }

// SQL returns the underlying *sql.DB. Exposed so callers in other packages
// (notably web.Server, which needs raw access for vault_secrets and session
// helpers) can run ad-hoc queries without DB having to grow a wrapper method
// for every shape. Use sparingly — prefer adding a typed method on *DB when
// the query has more than one call site.
func (d *DB) SQL() *sql.DB { return d.sql }

// ── Schema ────────────────────────────────────────────────────────────────────

func (d *DB) migrate() error {
	_, err := d.sql.Exec(`
	CREATE TABLE IF NOT EXISTS conversations (
		id          TEXT PRIMARY KEY,
		user_name   TEXT NOT NULL,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS messages (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		conversation_id TEXT NOT NULL REFERENCES conversations(id),
		role            TEXT NOT NULL,  -- user | assistant | system
		content         TEXT NOT NULL,
		category        TEXT,
		policy_action   TEXT,           -- allow | block | request_approval | pending
		created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS approvals (
		id             TEXT PRIMARY KEY,
		user_name      TEXT NOT NULL,
		user_display   TEXT NOT NULL,
		age_group      TEXT NOT NULL,
		category       TEXT NOT NULL,
		query_text     TEXT NOT NULL,
		status         TEXT NOT NULL DEFAULT 'pending', -- pending|approved|denied|expired
		created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at     DATETIME NOT NULL,
		decided_by     TEXT,
		decision_note  TEXT
	);

	CREATE TABLE IF NOT EXISTS skills (
		id              TEXT PRIMARY KEY,   -- slug, e.g. "web-search"
		name            TEXT NOT NULL,
		description     TEXT,
		source_url      TEXT,
		version         TEXT,
		enabled         INTEGER NOT NULL DEFAULT 1,
		seccheck_score  INTEGER,
		seccheck_verdict TEXT,
		installed_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS seccheck_reports (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		skill_id    TEXT,
		repo_url    TEXT NOT NULL,
		commit_sha  TEXT,
		score       INTEGER NOT NULL,
		verdict     TEXT NOT NULL,
		summary     TEXT NOT NULL,
		report_json TEXT NOT NULL,       -- full JSON report
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS gateway_accounts (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		user_name   TEXT NOT NULL,
		gateway     TEXT NOT NULL,
		external_id TEXT NOT NULL,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(gateway, external_id)
	);

	CREATE INDEX IF NOT EXISTS idx_messages_conv ON messages(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_approvals_status ON approvals(status);
	CREATE INDEX IF NOT EXISTS idx_approvals_user ON approvals(user_name);
	CREATE INDEX IF NOT EXISTS idx_gateway_accounts_lookup ON gateway_accounts(gateway, external_id);

	CREATE TABLE IF NOT EXISTS unknown_accounts (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		gateway      TEXT NOT NULL,
		external_id  TEXT NOT NULL,
		display_name TEXT NOT NULL DEFAULT '',
		first_seen   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_seen    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		attempts     INTEGER NOT NULL DEFAULT 1,
		UNIQUE(gateway, external_id)
	);
	CREATE INDEX IF NOT EXISTS idx_unknown_accounts_lookup ON unknown_accounts(gateway, external_id);

	CREATE TABLE IF NOT EXISTS used_tokens (
		token_hash TEXT PRIMARY KEY,
		used_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_used_tokens_used_at ON used_tokens(used_at);

	CREATE TABLE IF NOT EXISTS installed_skills (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		name            TEXT NOT NULL UNIQUE,
		repo_url        TEXT NOT NULL,
		installed_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		installed_by    TEXT,
		version         TEXT NOT NULL,
		version_sha     TEXT NOT NULL,
		tool_def_hash   TEXT,
		hb_verdict      TEXT,
		update_policy   TEXT NOT NULL DEFAULT 'ask',
		last_checked    DATETIME,
		update_available TEXT,
		previous_version TEXT,
		previous_sha    TEXT,
		disabled        BOOLEAN DEFAULT FALSE,
		forced_install  BOOLEAN DEFAULT FALSE
	);

	CREATE TABLE IF NOT EXISTS skill_update_checks (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		skill_name      TEXT NOT NULL,
		checked_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		current_version TEXT NOT NULL,
		found_version   TEXT,
		verdict         TEXT,
		tool_def_changed BOOLEAN DEFAULT FALSE,
		installed       BOOLEAN DEFAULT FALSE,
		notes           TEXT
	);

	CREATE TABLE IF NOT EXISTS quarantine (
		scan_target  TEXT PRIMARY KEY,
		tool_name    TEXT NOT NULL,
		verdict      TEXT NOT NULL,
		reasoning    TEXT,
		key_finding  TEXT,
		blocked_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS audit_log (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		actor_name  TEXT NOT NULL,
		gateway     TEXT NOT NULL,
		tool_name   TEXT NOT NULL,
		args        TEXT NOT NULL,
		ts          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_audit_log_actor ON audit_log(actor_name);
	CREATE INDEX IF NOT EXISTS idx_audit_log_ts ON audit_log(ts);

	CREATE TABLE IF NOT EXISTS user_role_overrides (
		user_name   TEXT PRIMARY KEY,
		role        TEXT NOT NULL,
		age_group   TEXT NOT NULL,
		set_by      TEXT NOT NULL,
		set_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS web_sessions (
		id          TEXT PRIMARY KEY NOT NULL,
		user_id     INTEGER NOT NULL,
		created_at  INTEGER NOT NULL,
		expires_at  INTEGER NOT NULL,
		last_seen   INTEGER NOT NULL,
		ip          TEXT NOT NULL DEFAULT '',
		user_agent  TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_web_sessions_expires_at ON web_sessions(expires_at);

	CREATE TABLE IF NOT EXISTS vault_secrets (
		name        TEXT PRIMARY KEY NOT NULL,
		ciphertext  BLOB NOT NULL,
		updated_at  INTEGER NOT NULL DEFAULT 0
	);

	-- Phase 2 — tool result spillover cache + audit log.
	-- See docs/superpowers/specs/2026-05-12-context-management-design.md §3.
	CREATE TABLE IF NOT EXISTS tool_result_cache (
		id            TEXT PRIMARY KEY,
		user_name     TEXT NOT NULL,
		conv_id       TEXT NOT NULL,
		tool_name     TEXT NOT NULL,
		args_hash     TEXT NOT NULL,
		payload_path  TEXT NOT NULL,
		bytes         INTEGER NOT NULL,
		content_type  TEXT NOT NULL,
		created_at    INTEGER NOT NULL,
		expires_at    INTEGER NOT NULL,
		accessed_at   INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_tool_cache_user_conv ON tool_result_cache (user_name, conv_id);
	CREATE INDEX IF NOT EXISTS idx_tool_cache_dedup     ON tool_result_cache (user_name, tool_name, args_hash);
	CREATE INDEX IF NOT EXISTS idx_tool_cache_expires   ON tool_result_cache (expires_at);

	CREATE TABLE IF NOT EXISTS tool_result_audit (
		id                TEXT PRIMARY KEY,
		user_name         TEXT NOT NULL,
		conv_id           TEXT NOT NULL,
		tool_name         TEXT NOT NULL,
		args_hash         TEXT NOT NULL,
		args_summary      TEXT NOT NULL,
		bytes             INTEGER NOT NULL,
		content_type      TEXT NOT NULL,
		category          TEXT,
		created_at        INTEGER NOT NULL,
		payload_id        TEXT,
		payload_purged_at INTEGER
	);
	CREATE INDEX IF NOT EXISTS idx_tool_audit_user_conv ON tool_result_audit (user_name, conv_id);
	CREATE INDEX IF NOT EXISTS idx_tool_audit_created   ON tool_result_audit (created_at);

	-- Phase 3.3 — family state (shared family memory).
	-- See docs/superpowers/specs/2026-05-13-family-state-design.md.
	CREATE TABLE IF NOT EXISTS family_fact_categories (
		name          TEXT PRIMARY KEY,
		description   TEXT NOT NULL,
		always_inject INTEGER NOT NULL DEFAULT 0,
		is_builtin    INTEGER NOT NULL DEFAULT 0,
		created_at    INTEGER NOT NULL,
		updated_at    INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS family_facts (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		category   TEXT NOT NULL REFERENCES family_fact_categories(name) ON DELETE RESTRICT,
		subject    TEXT NOT NULL,
		label      TEXT NOT NULL,
		value      TEXT NOT NULL,
		recurrence TEXT DEFAULT NULL,
		created_by TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		UNIQUE(category, subject, label)
	);
	CREATE INDEX IF NOT EXISTS idx_family_facts_subject  ON family_facts(subject);
	CREATE INDEX IF NOT EXISTS idx_family_facts_category ON family_facts(category);

	-- Phase 3.4 — todo lists (per-user).
	CREATE TABLE IF NOT EXISTS todos (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_name  TEXT NOT NULL,
		text       TEXT NOT NULL,
		completed  INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_todos_user ON todos(user_name);
	CREATE INDEX IF NOT EXISTS idx_todos_user_completed ON todos(user_name, completed);

	-- Phase 4 — reminders (fire-once scheduled notifications).
	-- See internal/reminder/ for scheduler and delivery logic.
	CREATE TABLE IF NOT EXISTS reminders (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		user_name      TEXT NOT NULL,
		gateway        TEXT NOT NULL,
		external_id    TEXT NOT NULL,
		group_id       TEXT DEFAULT '',
		is_group       INTEGER NOT NULL DEFAULT 0,
		message        TEXT NOT NULL,
		due_at         TEXT NOT NULL,
		dispatched     INTEGER NOT NULL DEFAULT 0,
		dispatched_at  TEXT DEFAULT '',
		created_at     TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_reminders_due_at ON reminders(due_at);
	CREATE INDEX IF NOT EXISTS idx_reminders_user_dispatched ON reminders(user_name, dispatched);
	`)
	if err != nil {
		return err
	}

	// Guard for existing deployments that predate the decision_note column.
	// SQLite does not support ADD COLUMN IF NOT EXISTS, so we attempt the
	// ALTER TABLE and ignore the error when the column already exists.
	if _, err := d.sql.ExecContext(context.Background(), `ALTER TABLE approvals ADD COLUMN decision_note TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate add decision_note: %w", err)
		}
	}

	// Guard for existing deployments that predate the reminders table migration.
	// SQLite does not support ADD COLUMN IF NOT EXISTS, so we attempt the
	// ALTER TABLE and ignore the error when the column already exists.
	if _, err := d.sql.ExecContext(context.Background(), `ALTER TABLE reminders ADD COLUMN dispatched INTEGER NOT NULL DEFAULT 0`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate add reminders dispatched: %w", err)
		}
	}

	// Phase 3.3 seed: built-in family_fact_categories. Idempotent.
	now := time.Now().Unix()
	if _, err := d.sql.ExecContext(context.Background(), `
		INSERT INTO family_fact_categories (name, description, always_inject, is_builtin, created_at, updated_at)
		VALUES
		  ('allergies', 'Per-person allergies and severity. Always visible to the assistant for safety.', 1, 1, ?, ?),
		  ('dietary_restrictions', 'Per-person or family dietary patterns (vegetarian, kosher, halal, gluten-free, etc.). Always visible to the assistant.', 1, 1, ?, ?),
		  ('important_dates', 'Birthdays, anniversaries, recurring family events. Read on demand. Phase 3.1 reminders read this table.', 0, 1, ?, ?),
		  ('pets', 'Family pets — names, species, notes. Read on demand.', 0, 1, ?, ?)
		ON CONFLICT(name) DO NOTHING`,
		now, now, now, now, now, now, now, now); err != nil {
		return fmt.Errorf("migrate seed family_fact_categories: %w", err)
	}

	return nil
}

// ── Quarantine ───────────────────────────────────────────────────────────────

// QuarantineEntry represents a blocked tool.
type QuarantineEntry struct {
	ScanTarget string
	ToolName   string
	Verdict    string
	Reasoning  string
	KeyFinding string
	BlockedAt  time.Time
}

// ListQuarantine returns all quarantined entries.
func (d *DB) ListQuarantine() ([]QuarantineEntry, error) {
	rows, err := d.sql.Query(`SELECT scan_target, tool_name, verdict, reasoning, key_finding, blocked_at FROM quarantine`)
	if err != nil {
		return nil, fmt.Errorf("listing quarantine: %w", err)
	}
	defer rows.Close()

	var entries []QuarantineEntry
	for rows.Next() {
		var e QuarantineEntry
		if err := rows.Scan(&e.ScanTarget, &e.ToolName, &e.Verdict, &e.Reasoning, &e.KeyFinding, &e.BlockedAt); err != nil {
			return nil, fmt.Errorf("scanning quarantine row: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// UpsertQuarantine adds or updates a quarantine entry.
func (d *DB) UpsertQuarantine(e QuarantineEntry) error {
	_, err := d.sql.Exec(`
		INSERT INTO quarantine (scan_target, tool_name, verdict, reasoning, key_finding, blocked_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(scan_target) DO UPDATE SET
			tool_name = excluded.tool_name,
			verdict = excluded.verdict,
			reasoning = excluded.reasoning,
			key_finding = excluded.key_finding,
			blocked_at = excluded.blocked_at`,
		e.ScanTarget, e.ToolName, e.Verdict, e.Reasoning, e.KeyFinding, e.BlockedAt)
	if err != nil {
		return fmt.Errorf("upserting quarantine: %w", err)
	}
	return nil
}

// DeleteQuarantine removes a quarantine entry.
func (d *DB) DeleteQuarantine(scanTarget string) error {
	_, err := d.sql.Exec(`DELETE FROM quarantine WHERE scan_target = ?`, scanTarget)
	if err != nil {
		return fmt.Errorf("deleting quarantine: %w", err)
	}
	return nil
}

// ── Approvals ─────────────────────────────────────────────────────────────────

type Approval struct {
	ID           string `json:"id"`
	UserName     string `json:"user_name"`
	UserDisplay  string `json:"user_display"`
	AgeGroup     string `json:"age_group"`
	Category     string `json:"category"`
	QueryText    string `json:"query_text"`
	Status       string `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	DecidedBy    string `json:"decided_by"`
	DecisionNote string `json:"decision_note"`
}

// UpsertApproval inserts a new approval request or returns the existing one.
// Returns (isNew, error).
func (d *DB) UpsertApproval(a *Approval) (bool, error) {
	var count int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM approvals WHERE id = ?`, a.ID).Scan(&count)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil // already exists
	}

	_, err = d.sql.Exec(`
		INSERT INTO approvals (id, user_name, user_display, age_group, category, query_text, status, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?)`,
		a.ID, a.UserName, a.UserDisplay, a.AgeGroup, a.Category, a.QueryText,
		time.Now().UTC().Add(time.Duration(24)*time.Hour))
	return err == nil, err
}

func (d *DB) GetApproval(ctx context.Context, id string) (*Approval, error) {
	a := &Approval{}
	err := d.sql.QueryRowContext(ctx, `
		SELECT id, user_name, user_display, age_group, category, query_text,
		       status, created_at, updated_at, expires_at,
		       COALESCE(decided_by,''), COALESCE(decision_note,'')
		FROM approvals WHERE id = ?`, id).Scan(
		&a.ID, &a.UserName, &a.UserDisplay, &a.AgeGroup, &a.Category, &a.QueryText,
		&a.Status, &a.CreatedAt, &a.UpdatedAt, &a.ExpiresAt,
		&a.DecidedBy, &a.DecisionNote)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func (d *DB) DecideApproval(id, status, decidedBy string) error {
	res, err := d.sql.Exec(`
		UPDATE approvals SET status=?, decided_by=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND status='pending'`, status, decidedBy, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("approval %s not found or already decided", id)
	}
	return nil
}

// DecideApprovalWithNote updates an approval's status and sets an optional
// decision note. The `AND status='pending'` guard prevents two parents from
// racing to overwrite a prior decision (or the same parent from accidentally
// flipping approve→deny). Returns ErrApprovalAlreadyDecided if no pending
// row matches.
func (d *DB) DecideApprovalWithNote(ctx context.Context, id, status, decidedBy, note string) error {
	res, err := d.sql.ExecContext(ctx, `
		UPDATE approvals SET status = ?, decided_by = ?, updated_at = CURRENT_TIMESTAMP, decision_note = ?
		WHERE id = ? AND status = 'pending'`, status, decidedBy, note, id)
	if err != nil {
		return fmt.Errorf("decide approval with note: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("decide approval with note %s: %w", id, ErrApprovalAlreadyDecided)
	}
	return nil
}

func (d *DB) PendingApprovals(ctx context.Context) ([]*Approval, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, user_name, user_display, age_group, category, query_text,
		       status, created_at, updated_at, expires_at,
		       COALESCE(decided_by,''), COALESCE(decision_note,'')
		FROM approvals WHERE status='pending' ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanApprovals(rows)
}

func (d *DB) RecentApprovals(limit int) ([]*Approval, error) {
	rows, err := d.sql.Query(`
		SELECT id, user_name, user_display, age_group, category, query_text,
		       status, created_at, updated_at, expires_at,
		       COALESCE(decided_by,''), COALESCE(decision_note,'')
		FROM approvals ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanApprovals(rows)
}

// AllApprovalsForOPA returns approvals as map[id]→{status,...} for OPA input.
func (d *DB) AllApprovalsForOPA() (map[string]any, error) {
	rows, err := d.sql.Query(`SELECT id, status, COALESCE(decided_by,'') FROM approvals`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]any)
	for rows.Next() {
		var id, status, decidedBy string
		if err := rows.Scan(&id, &status, &decidedBy); err != nil {
			continue
		}
		out[id] = map[string]any{"status": status, "decided_by": decidedBy}
	}
	return out, nil
}

func scanApprovals(rows *sql.Rows) ([]*Approval, error) {
	var out []*Approval
	for rows.Next() {
		a := &Approval{}
		err := rows.Scan(&a.ID, &a.UserName, &a.UserDisplay, &a.AgeGroup,
			&a.Category, &a.QueryText, &a.Status,
			&a.CreatedAt, &a.UpdatedAt, &a.ExpiresAt,
			&a.DecidedBy, &a.DecisionNote)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AllApprovals returns all approval records in the database.
func (d *DB) AllApprovals() ([]*Approval, error) {
	rows, err := d.sql.Query(`SELECT id, user_name, user_display, age_group, category, query_text, status, created_at, updated_at, expires_at, COALESCE(decided_by,''), COALESCE(decision_note,'') FROM approvals`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanApprovals(rows)
}

// ── Conversations ─────────────────────────────────────────────────────────────

type Message struct {
	ID             int64
	ConversationID string
	Role           string
	Content        string
	Category       string
	PolicyAction   string
	CreatedAt      time.Time
}

func (d *DB) SaveMessage(convID, userName, role, content, category, policyAction string) error {
	// Ensure conversation exists
	d.sql.Exec(`INSERT OR IGNORE INTO conversations (id, user_name) VALUES (?, ?)`, convID, userName) //nolint:errcheck

	_, err := d.sql.Exec(`
		INSERT INTO messages (conversation_id, role, content, category, policy_action)
		VALUES (?, ?, ?, ?, ?)`,
		convID, role, content, category, policyAction)
	return err
}

func (d *DB) GetConversationHistory(convID string, limit int) ([]*Message, error) {
	rows, err := d.sql.Query(`
		SELECT id, conversation_id, role, content, COALESCE(category,''), COALESCE(policy_action,''), created_at
		FROM messages WHERE conversation_id=?
		ORDER BY created_at DESC LIMIT ?`, convID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []*Message
	for rows.Next() {
		m := &Message{}
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content,
			&m.Category, &m.PolicyAction, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	// Reverse to chronological order
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, rows.Err()
}

// RecentMessagesByUser returns the most recent messages for a specific user across all conversations.
func (d *DB) RecentMessagesByUser(userName string, limit int) ([]*Message, error) {
	rows, err := d.sql.Query(`
		SELECT m.id, m.conversation_id, m.role, m.content,
		       COALESCE(m.category,''), COALESCE(m.policy_action,''), m.created_at
		FROM messages m
		JOIN conversations c ON c.id = m.conversation_id
		WHERE c.user_name = ?
		ORDER BY m.created_at DESC LIMIT ?`, userName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []*Message
	for rows.Next() {
		m := &Message{}
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content,
			&m.Category, &m.PolicyAction, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// ── Skills ────────────────────────────────────────────────────────────────────

type Skill struct {
	ID              string
	Name            string
	Description     string
	SourceURL       string
	Version         string
	Enabled         bool
	SecCheckScore   int
	SecCheckVerdict string
	InstalledAt     time.Time
}

func (d *DB) UpsertSkill(s *Skill) error {
	_, err := d.sql.Exec(`
		INSERT INTO skills (id, name, description, source_url, version, enabled, seccheck_score, seccheck_verdict)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, description=excluded.description,
			version=excluded.version, seccheck_score=excluded.seccheck_score,
			seccheck_verdict=excluded.seccheck_verdict, updated_at=CURRENT_TIMESTAMP`,
		s.ID, s.Name, s.Description, s.SourceURL, s.Version,
		s.Enabled, s.SecCheckScore, s.SecCheckVerdict)
	return err
}

func (d *DB) ListSkills() ([]*Skill, error) {
	rows, err := d.sql.Query(`
		SELECT id, name, COALESCE(description,''), COALESCE(source_url,''),
		       COALESCE(version,''), enabled, COALESCE(seccheck_score,0),
		       COALESCE(seccheck_verdict,''), installed_at
		FROM skills ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Skill
	for rows.Next() {
		s := &Skill{}
		var enabled int
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.SourceURL,
			&s.Version, &enabled, &s.SecCheckScore, &s.SecCheckVerdict, &s.InstalledAt); err != nil {
			return nil, err
		}
		s.Enabled = enabled == 1
		out = append(out, s)
	}
	return out, rows.Err()
}

// ── Gateway Accounts ──────────────────────────────────────────────────────────

func (d *DB) LinkGatewayAccount(userName, gateway, externalID string) error {
	_, err := d.sql.Exec(`
		INSERT INTO gateway_accounts (user_name, gateway, external_id)
		VALUES (?, ?, ?)
		ON CONFLICT(gateway, external_id) DO UPDATE SET user_name=excluded.user_name`,
		userName, gateway, externalID)
	if err != nil {
		return fmt.Errorf("linking gateway account: %w", err)
	}
	return nil
}

func (d *DB) ResolveGatewayAccount(ctx context.Context, gateway, externalID string) (string, error) {
	var userName string
	err := d.sql.QueryRowContext(ctx,
		`SELECT user_name FROM gateway_accounts WHERE gateway=? AND external_id=?`,
		gateway, externalID).Scan(&userName)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("resolving gateway account: %w", err)
	}
	return userName, nil
}

func (d *DB) IsGatewayAccountRegistered(gateway, externalID string) bool {
	var count int
	d.sql.QueryRow(`SELECT COUNT(*) FROM gateway_accounts WHERE gateway=? AND external_id=?`,
		gateway, externalID).Scan(&count) //nolint:errcheck
	return count > 0
}

// GatewayAccount represents a linked gateway account row.
type GatewayAccount struct {
	ID         int64     `json:"id"`
	UserName   string    `json:"user_name"`
	Gateway    string    `json:"gateway"`
	ExternalID string    `json:"external_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// ListGatewayAccountsByUser returns all gateway accounts linked to a given user name.
func (d *DB) ListGatewayAccountsByUser(ctx context.Context, userName string) ([]GatewayAccount, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT id, user_name, gateway, external_id, created_at FROM gateway_accounts WHERE user_name = ?`,
		userName)
	if err != nil {
		return nil, fmt.Errorf("list gateway accounts by user: %w", err)
	}
	defer rows.Close()
	var out []GatewayAccount
	for rows.Next() {
		var g GatewayAccount
		if err := rows.Scan(&g.ID, &g.UserName, &g.Gateway, &g.ExternalID, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("list gateway accounts by user: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list gateway accounts by user: %w", err)
	}
	return out, nil
}

// HasGatewayAccount reports whether userName has any account linked
// for the given gateway. Used during gateway self-registration to
// filter the family-config user list to those still needing a link.
func (d *DB) HasGatewayAccount(userName, gateway string) bool {
	var count int
	err := d.sql.QueryRow(
		`SELECT COUNT(*) FROM gateway_accounts WHERE user_name=? AND gateway=?`,
		userName, gateway).Scan(&count)
	if err != nil {
		return false
	}
	return count > 0
}

// ── Unknown Accounts ──────────────────────────────────────────────────────────

type UnknownAccount struct {
	ID          int64     `json:"id"`
	Gateway     string    `json:"gateway"`
	ExternalID  string    `json:"external_id"`
	DisplayName string    `json:"display_name"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	Attempts    int       `json:"attempts"`
}

func (d *DB) RecordUnknownAccount(ctx context.Context, gateway, externalID, displayName string) error {
	gw := strings.ToLower(gateway)
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO unknown_accounts (gateway, external_id, display_name)
		VALUES (?, ?, ?)
		ON CONFLICT(gateway, external_id) DO UPDATE SET
			last_seen = CURRENT_TIMESTAMP,
			attempts  = attempts + 1,
			display_name = CASE WHEN unknown_accounts.display_name = ''
			                    THEN excluded.display_name
			                    ELSE unknown_accounts.display_name END`,
		gw, externalID, displayName)
	if err != nil {
		return fmt.Errorf("recording unknown account: %w", err)
	}
	return nil
}

func (d *DB) ListUnknownAccounts(ctx context.Context) ([]UnknownAccount, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, gateway, external_id, display_name, first_seen, last_seen, attempts
		FROM unknown_accounts ORDER BY last_seen DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing unknown accounts: %w", err)
	}
	defer rows.Close()
	var out []UnknownAccount
	for rows.Next() {
		var u UnknownAccount
		if err := rows.Scan(&u.ID, &u.Gateway, &u.ExternalID, &u.DisplayName,
			&u.FirstSeen, &u.LastSeen, &u.Attempts); err != nil {
			return nil, fmt.Errorf("scanning unknown account: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating unknown accounts: %w", err)
	}
	return out, nil
}

func (d *DB) DeleteUnknownAccount(ctx context.Context, gateway, externalID string) error {
	_, err := d.sql.ExecContext(ctx,
		`DELETE FROM unknown_accounts WHERE gateway=? AND external_id=?`,
		strings.ToLower(gateway), externalID)
	if err != nil {
		return fmt.Errorf("deleting unknown account: %w", err)
	}
	return nil
}

// LinkAndClearUnknownAccount links a gateway account to a user AND deletes
// the matching unknown_accounts row in a single transaction. Either both
// changes commit or neither does — preventing the operator UI from showing
// a stale "unknown" entry for an already-linked account.
func (d *DB) LinkAndClearUnknownAccount(ctx context.Context, userName, gateway, externalID string) error {
	gw := strings.ToLower(gateway)
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin link+clear tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO gateway_accounts (user_name, gateway, external_id)
		VALUES (?, ?, ?)
		ON CONFLICT(gateway, external_id) DO UPDATE SET user_name=excluded.user_name`,
		userName, gw, externalID); err != nil {
		return fmt.Errorf("linking gateway account: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM unknown_accounts WHERE gateway=? AND external_id=?`,
		gw, externalID); err != nil {
		return fmt.Errorf("clearing unknown account: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit link+clear tx: %w", err)
	}
	return nil
}

func (d *DB) UnlinkGatewayAccount(gateway, externalID string) error {
	_, err := d.sql.Exec(`DELETE FROM gateway_accounts WHERE gateway=? AND external_id=?`,
		gateway, externalID)
	if err != nil {
		return fmt.Errorf("unlinking gateway account: %w", err)
	}
	return nil
}

// ── Token Replay Protection ───────────────────────────────────────────────────

// MarkTokenUsed records a token hash as used. Returns false if already used.
func (d *DB) MarkTokenUsed(tokenHash string) (bool, error) {
	_, err := d.sql.Exec(`INSERT OR IGNORE INTO used_tokens (token_hash) VALUES (?)`, tokenHash)
	if err != nil {
		return false, fmt.Errorf("marking token used: %w", err)
	}
	// Check if it was actually inserted (not already there)
	var count int
	d.sql.QueryRow(`SELECT COUNT(*) FROM used_tokens WHERE token_hash = ?`, tokenHash).Scan(&count)
	return count == 1, nil
}

// IsTokenUsed checks if a token hash has been used before.
func (d *DB) IsTokenUsed(tokenHash string) bool {
	var count int
	d.sql.QueryRow(`SELECT COUNT(*) FROM used_tokens WHERE token_hash = ?`, tokenHash).Scan(&count)
	return count > 0
}

// CleanupOldTokens removes used token records older than the given duration.
func (d *DB) CleanupOldTokens(olderThan time.Duration) error {
	cutoff := time.Now().Add(-olderThan)
	_, err := d.sql.Exec(`DELETE FROM used_tokens WHERE used_at < ?`, cutoff)
	return err
}

// ── Installed Skills ──────────────────────────────────────────────────────────

type InstalledSkill struct {
	ID              int64
	Name            string
	RepoURL         string
	InstalledAt     time.Time
	InstalledBy     string
	Version         string
	VersionSHA      string
	ToolDefHash     string
	HBVerdict       string
	UpdatePolicy    string // ask | auto | pin | disabled
	LastChecked     time.Time
	UpdateAvailable string
	PreviousVersion string
	PreviousSHA     string
	Disabled        bool
	ForcedInstall   bool
}

func (d *DB) UpsertInstalledSkill(s *InstalledSkill) error {
	_, err := d.sql.Exec(`
		INSERT INTO installed_skills (name, repo_url, installed_by, version, version_sha, tool_def_hash, hb_verdict, update_policy, forced_install)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			repo_url=excluded.repo_url, version=excluded.version, version_sha=excluded.version_sha,
			tool_def_hash=excluded.tool_def_hash, hb_verdict=excluded.hb_verdict,
			previous_version=installed_skills.version, previous_sha=installed_skills.version_sha`,
		s.Name, s.RepoURL, s.InstalledBy, s.Version, s.VersionSHA, s.ToolDefHash, s.HBVerdict, s.UpdatePolicy, s.ForcedInstall)
	return err
}

func (d *DB) GetInstalledSkill(name string) (*InstalledSkill, error) {
	s := &InstalledSkill{}
	err := d.sql.QueryRow(`
		SELECT id, name, repo_url, installed_at, COALESCE(installed_by,''), version, version_sha,
		       COALESCE(tool_def_hash,''), COALESCE(hb_verdict,''), update_policy,
		       COALESCE(previous_version,''), COALESCE(previous_sha,''), disabled, forced_install
		FROM installed_skills WHERE name = ?`, name).Scan(
		&s.ID, &s.Name, &s.RepoURL, &s.InstalledAt, &s.InstalledBy, &s.Version, &s.VersionSHA,
		&s.ToolDefHash, &s.HBVerdict, &s.UpdatePolicy,
		&s.PreviousVersion, &s.PreviousSHA, &s.Disabled, &s.ForcedInstall)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return s, err
}

func (d *DB) ListInstalledSkills() ([]*InstalledSkill, error) {
	rows, err := d.sql.Query(`
		SELECT id, name, repo_url, installed_at, COALESCE(installed_by,''), version, version_sha,
		       COALESCE(tool_def_hash,''), COALESCE(hb_verdict,''), update_policy,
		       COALESCE(previous_version,''), COALESCE(previous_sha,''), disabled, forced_install
		FROM installed_skills ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*InstalledSkill
	for rows.Next() {
		s := &InstalledSkill{}
		if err := rows.Scan(&s.ID, &s.Name, &s.RepoURL, &s.InstalledAt, &s.InstalledBy,
			&s.Version, &s.VersionSHA, &s.ToolDefHash, &s.HBVerdict, &s.UpdatePolicy,
			&s.PreviousVersion, &s.PreviousSHA, &s.Disabled, &s.ForcedInstall); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) SetSkillUpdatePolicy(name, policy string) error {
	_, err := d.sql.Exec(`UPDATE installed_skills SET update_policy = ? WHERE name = ?`, policy, name)
	return err
}

func (d *DB) DisableInstalledSkill(name string) error {
	_, err := d.sql.Exec(`UPDATE installed_skills SET disabled = TRUE WHERE name = ?`, name)
	return err
}

func (d *DB) LogUpdateCheck(skillName, currentVersion, foundVersion, verdict, notes string, toolDefChanged bool) error {
	_, err := d.sql.Exec(`
		INSERT INTO skill_update_checks (skill_name, current_version, found_version, verdict, tool_def_changed, notes)
		VALUES (?, ?, ?, ?, ?, ?)`,
		skillName, currentVersion, foundVersion, verdict, toolDefChanged, notes)
	return err
}

// ── SecCheck Reports ──────────────────────────────────────────────────────────

func (d *DB) SaveSecCheckReport(skillID, repoURL, commitSHA string, score int, verdict, summary, reportJSON string) error {
	_, err := d.sql.Exec(`
		INSERT INTO seccheck_reports (skill_id, repo_url, commit_sha, score, verdict, summary, report_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		skillID, repoURL, commitSHA, score, verdict, summary, reportJSON)
	return err
}

// ── Audit Log ─────────────────────────────────────────────────────────────────

// AuditLog is one row from the audit_log table.
type AuditLog struct {
	ID        int64
	ActorName string
	Gateway   string
	ToolName  string
	Args      string // JSON-encoded payload, as stored
	Ts        time.Time
}

// LogAudit records an admin tool invocation to the audit_log table.
func (d *DB) LogAudit(ctx context.Context, actorName, gateway, toolName string, args []byte) error {
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO audit_log (actor_name, gateway, tool_name, args) VALUES (?, ?, ?, ?)`,
		actorName, gateway, toolName, string(args))
	if err != nil {
		return fmt.Errorf("log audit: %w", err)
	}
	return nil
}

// ListAuditLogs returns the most recent audit_log rows, newest first. A
// non-positive limit is clamped to 100.
func (d *DB) ListAuditLogs(ctx context.Context, limit int) ([]*AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.sql.QueryContext(ctx,
		`SELECT id, actor_name, gateway, tool_name, args, ts
		 FROM audit_log ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()
	var out []*AuditLog
	for rows.Next() {
		a := &AuditLog{}
		if err := rows.Scan(&a.ID, &a.ActorName, &a.Gateway, &a.ToolName, &a.Args, &a.Ts); err != nil {
			return nil, fmt.Errorf("scan audit log: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ── Role Overrides ────────────────────────────────────────────────────────────

// GetRoleOverride returns the role override for a user, or ("", "", nil) if none exists.
func (d *DB) GetRoleOverride(ctx context.Context, userName string) (role, ageGroup string, err error) {
	err = d.sql.QueryRowContext(ctx,
		`SELECT role, age_group FROM user_role_overrides WHERE user_name = ?`, userName).
		Scan(&role, &ageGroup)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("get role override: %w", err)
	}
	return role, ageGroup, nil
}

// GetEffectiveRoleAge returns the effective (overridden) role and age group for a user.
// If no override exists, the returned values match the config defaults.
// Returns ("", "", nil) when no row exists for the user.
func (d *DB) GetEffectiveRoleAge(ctx context.Context, userName string, cfgRole, cfgAgeGroup string) (role, ageGroup string, err error) {
	role, ageGroup, err = d.GetRoleOverride(ctx, userName)
	if err != nil {
		return "", "", err
	}
	if role == "" {
		role = cfgRole
	}
	if ageGroup == "" {
		ageGroup = cfgAgeGroup
	}
	return role, ageGroup, nil
}

// SetRoleOverride upserts a role override for a user.
func (d *DB) SetRoleOverride(ctx context.Context, userName, role, ageGroup, setBy string) error {
	_, err := d.sql.ExecContext(ctx,
		`INSERT OR REPLACE INTO user_role_overrides (user_name, role, age_group, set_by, set_at) VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		userName, role, ageGroup, setBy)
	if err != nil {
		return fmt.Errorf("set role override: %w", err)
	}
	return nil
}

// DeleteRoleOverride removes the role override for a user.
func (d *DB) DeleteRoleOverride(ctx context.Context, userName string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM user_role_overrides WHERE user_name = ?`, userName)
	if err != nil {
		return fmt.Errorf("delete role override: %w", err)
	}
	return nil
}

// ── Todos ────────────────────────────────────────────────────────────────────

// Todo represents a todo item for a user.
type Todo struct {
	ID        int64
	UserName  string
	Text      string
	Completed bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AddTodo inserts a new todo item for the given user.
func (d *DB) AddTodo(ctx context.Context, userName, text string) (*Todo, error) {
	now := time.Now().Unix()
	res, err := d.sql.ExecContext(ctx,
		`INSERT INTO todos (user_name, text, completed, created_at, updated_at) VALUES (?, ?, 0, ?, ?)`,
		userName, text, now, now)
	if err != nil {
		return nil, fmt.Errorf("add todo: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("add todo last insert id: %w", err)
	}
	return &Todo{
		ID:        id,
		UserName:  userName,
		Text:      text,
		Completed: false,
		CreatedAt: time.Unix(now, 0),
		UpdatedAt: time.Unix(now, 0),
	}, nil
}

// ListTodos returns all todos for the given user, optionally filtered by completion status.
func (d *DB) ListTodos(ctx context.Context, userName string, completed *bool) ([]*Todo, error) {
	q := `SELECT id, user_name, text, completed, created_at, updated_at FROM todos WHERE user_name = ?`
	args := []any{userName}
	if completed != nil {
		q += ` AND completed = ?`
		if *completed {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	q += ` ORDER BY completed ASC, created_at DESC`

	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list todos: %w", err)
	}
	defer rows.Close()

	var out []*Todo
	for rows.Next() {
		t, err := scanTodo(rows)
		if err != nil {
			return nil, fmt.Errorf("scan todo: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CompleteTodo marks a todo as completed.
func (d *DB) CompleteTodo(ctx context.Context, userName string, id int64) error {
	now := time.Now().Unix()
	res, err := d.sql.ExecContext(ctx,
		`UPDATE todos SET completed = 1, updated_at = ? WHERE id = ? AND user_name = ?`,
		now, id, userName)
	if err != nil {
		return fmt.Errorf("complete todo: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("todo not found or not owned by user")
	}
	return nil
}

// UncompleteTodo marks a todo as not completed (reopen).
func (d *DB) UncompleteTodo(ctx context.Context, userName string, id int64) error {
	now := time.Now().Unix()
	res, err := d.sql.ExecContext(ctx,
		`UPDATE todos SET completed = 0, updated_at = ? WHERE id = ? AND user_name = ?`,
		now, id, userName)
	if err != nil {
		return fmt.Errorf("uncomplete todo: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("todo not found or not owned by user")
	}
	return nil
}

// RemoveTodo deletes a todo item.
func (d *DB) RemoveTodo(ctx context.Context, userName string, id int64) error {
	res, err := d.sql.ExecContext(ctx,
		`DELETE FROM todos WHERE id = ? AND user_name = ?`, id, userName)
	if err != nil {
		return fmt.Errorf("remove todo: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("todo not found or not owned by user")
	}
	return nil
}

func scanTodo(rows *sql.Rows) (*Todo, error) {
	var t Todo
	var createdAt, updatedAt int64
	var completed int
	if err := rows.Scan(&t.ID, &t.UserName, &t.Text, &completed, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	t.Completed = completed == 1
	t.CreatedAt = time.Unix(createdAt, 0)
	t.UpdatedAt = time.Unix(updatedAt, 0)
	return &t, nil
}

// ── Reminders ──────────────────────────────────────────────────────────────────

// Reminder represents a pending or dispatched reminder.
type Reminder struct {
	ID            int64
	UserName      string
	Gateway       string
	ExternalID    string
	GroupID       string
	IsGroup       bool
	Message       string
	DueAt         time.Time
	Dispatched    bool
	DispatchedAt  *time.Time
	CreatedAt     time.Time
}

// CreateReminder inserts a new reminder.
func (d *DB) CreateReminder(ctx context.Context, r *Reminder) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO reminders (user_name, gateway, external_id, group_id, is_group, message, due_at, dispatched, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, CURRENT_TIMESTAMP)`,
		r.UserName, r.Gateway, r.ExternalID, r.GroupID, boolToInt(r.IsGroup), r.Message, r.DueAt.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("creating reminder: %w", err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetDueReminders returns all reminders that are due and not yet dispatched.
func (d *DB) GetDueReminders(ctx context.Context, now time.Time) ([]*Reminder, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, user_name, gateway, external_id, group_id, is_group, message, due_at, dispatched, dispatched_at, created_at
		FROM reminders
		WHERE dispatched = 0 AND due_at <= ?
		ORDER BY due_at ASC`, now.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("querying due reminders: %w", err)
	}
	defer rows.Close()

	var out []*Reminder
	for rows.Next() {
		r := &Reminder{}
		var dueAtStr, dispatchedAtStr, createdAtStr string
		var dispatched, isGroup int
		if err := rows.Scan(&r.ID, &r.UserName, &r.Gateway, &r.ExternalID, &r.GroupID, &isGroup,
			&r.Message, &dueAtStr, &dispatched, &dispatchedAtStr, &createdAtStr); err != nil {
			return nil, fmt.Errorf("scanning reminder: %w", err)
		}
		r.DueAt, _ = time.Parse(time.RFC3339, dueAtStr)
		r.Dispatched = dispatched == 1
		r.IsGroup = isGroup == 1
		if dispatchedAtStr != "" {
			if t, err := time.Parse(time.RFC3339, dispatchedAtStr); err == nil {
				r.DispatchedAt = &t
			}
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkReminderDispatched marks a reminder as dispatched.
func (d *DB) MarkReminderDispatched(ctx context.Context, id int64, now time.Time) error {
	_, err := d.sql.ExecContext(ctx, `
		UPDATE reminders SET dispatched = 1, dispatched_at = ? WHERE id = ?`,
		now.UTC().Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("marking reminder dispatched: %w", err)
	}
	return nil
}

// GetPendingReminders returns all reminders that are not yet dispatched.
func (d *DB) GetPendingReminders(ctx context.Context) ([]*Reminder, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, user_name, gateway, external_id, group_id, is_group, message, due_at, dispatched, dispatched_at, created_at
		FROM reminders
		WHERE dispatched = 0
		ORDER BY due_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying pending reminders: %w", err)
	}
	defer rows.Close()

	var out []*Reminder
	for rows.Next() {
		r := &Reminder{}
		var dueAtStr, dispatchedAtStr, createdAtStr string
		var dispatched, isGroup int
		if err := rows.Scan(&r.ID, &r.UserName, &r.Gateway, &r.ExternalID, &r.GroupID, &isGroup,
			&r.Message, &dueAtStr, &dispatched, &dispatchedAtStr, &createdAtStr); err != nil {
			return nil, fmt.Errorf("scanning reminder: %w", err)
		}
		r.DueAt, _ = time.Parse(time.RFC3339, dueAtStr)
		r.Dispatched = dispatched == 1
		r.IsGroup = isGroup == 1
		if dispatchedAtStr != "" {
			if t, err := time.Parse(time.RFC3339, dispatchedAtStr); err == nil {
				r.DispatchedAt = &t
			}
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteReminder deletes a reminder by ID.
func (d *DB) DeleteReminder(ctx context.Context, id int64) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM reminders WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting reminder: %w", err)
	}
	return nil
}
