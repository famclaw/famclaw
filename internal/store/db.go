// Package store provides a SQLite-backed persistent store for FamClaw.
// Uses modernc.org/sqlite — pure Go, no CGO, cross-compiles to arm/arm64/android.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

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
	`)
	return err
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
	ID           string
	UserName     string
	UserDisplay  string
	AgeGroup     string
	Category     string
	QueryText    string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ExpiresAt    time.Time
	DecidedBy    string
	DecisionNote string
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

func (d *DB) GetApproval(id string) (*Approval, error) {
	a := &Approval{}
	err := d.sql.QueryRow(`
		SELECT id, user_name, user_display, age_group, category, query_text,
		       status, created_at, updated_at, expires_at,
		       COALESCE(decided_by,''), COALESCE(decision_note,'')
		FROM approvals WHERE id = ?`, id).Scan(
		&a.ID, &a.UserName, &a.UserDisplay, &a.AgeGroup, &a.Category, &a.QueryText,
		&a.Status, &a.CreatedAt, &a.UpdatedAt, &a.ExpiresAt,
		&a.DecidedBy, &a.DecisionNote)
	if err == sql.ErrNoRows {
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

func (d *DB) PendingApprovals() ([]*Approval, error) {
	rows, err := d.sql.Query(`
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

func (d *DB) ResolveGatewayAccount(gateway, externalID string) (string, error) {
	var userName string
	err := d.sql.QueryRow(`SELECT user_name FROM gateway_accounts WHERE gateway=? AND external_id=?`,
		gateway, externalID).Scan(&userName)
	if err == sql.ErrNoRows {
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

func (d *DB) RecordUnknownAccount(gateway, externalID, displayName string) error {
	gw := strings.ToLower(gateway)
	_, err := d.sql.Exec(`
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

func (d *DB) ListUnknownAccounts() ([]UnknownAccount, error) {
	rows, err := d.sql.Query(`
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
	return out, rows.Err()
}

func (d *DB) DeleteUnknownAccount(gateway, externalID string) error {
	_, err := d.sql.Exec(
		`DELETE FROM unknown_accounts WHERE gateway=? AND external_id=?`,
		strings.ToLower(gateway), externalID)
	if err != nil {
		return fmt.Errorf("deleting unknown account: %w", err)
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
