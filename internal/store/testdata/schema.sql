-- FamClaw schema golden file. Regenerate with:
--   go test -tags dumpschema -run TestDumpSchema ./internal/store/
-- (or set UPDATE_SCHEMA_GOLDEN=1 and run TestSchemaGolden)

CREATE INDEX idx_approvals_status ON approvals(status);

CREATE INDEX idx_approvals_user ON approvals(user_name);

CREATE INDEX idx_gateway_accounts_lookup ON gateway_accounts(gateway, external_id);

CREATE INDEX idx_messages_conv ON messages(conversation_id);

CREATE INDEX idx_unknown_accounts_lookup ON unknown_accounts(gateway, external_id);

CREATE INDEX idx_used_tokens_used_at ON used_tokens(used_at);

CREATE TABLE approvals (
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

CREATE TABLE conversations (
		id          TEXT PRIMARY KEY,
		user_name   TEXT NOT NULL,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

CREATE TABLE gateway_accounts (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		user_name   TEXT NOT NULL,
		gateway     TEXT NOT NULL,
		external_id TEXT NOT NULL,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(gateway, external_id)
	);

CREATE TABLE installed_skills (
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

CREATE TABLE messages (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		conversation_id TEXT NOT NULL REFERENCES conversations(id),
		role            TEXT NOT NULL,  -- user | assistant | system
		content         TEXT NOT NULL,
		category        TEXT,
		policy_action   TEXT,           -- allow | block | request_approval | pending
		created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

CREATE TABLE quarantine (
		scan_target  TEXT PRIMARY KEY,
		tool_name    TEXT NOT NULL,
		verdict      TEXT NOT NULL,
		reasoning    TEXT,
		key_finding  TEXT,
		blocked_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

CREATE TABLE seccheck_reports (
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

CREATE TABLE skill_update_checks (
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

CREATE TABLE skills (
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

CREATE TABLE unknown_accounts (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		gateway      TEXT NOT NULL,
		external_id  TEXT NOT NULL,
		display_name TEXT NOT NULL DEFAULT '',
		first_seen   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_seen    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		attempts     INTEGER NOT NULL DEFAULT 1,
		UNIQUE(gateway, external_id)
	);

CREATE TABLE used_tokens (
		token_hash TEXT PRIMARY KEY,
		used_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

