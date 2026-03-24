// Package store provides a SQLite-backed persistent store for FamClaw.
// Uses modernc.org/sqlite — pure Go, no CGO, cross-compiles to arm/arm64/android.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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

	CREATE INDEX IF NOT EXISTS idx_messages_conv ON messages(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_approvals_status ON approvals(status);
	CREATE INDEX IF NOT EXISTS idx_approvals_user ON approvals(user_name);
	`)
	return err
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

func (d *DB) SaveSecCheckReport(skillID, repoURL, commitSHA string, score int, verdict, summary, reportJSON string) error {
	_, err := d.sql.Exec(`
		INSERT INTO seccheck_reports (skill_id, repo_url, commit_sha, score, verdict, summary, report_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		skillID, repoURL, commitSHA, score, verdict, summary, reportJSON)
	return err
}
