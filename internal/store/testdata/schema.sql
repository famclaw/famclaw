CREATE INDEX idx_approvals_status ON approvals(status);

CREATE INDEX idx_approvals_user ON approvals(user_name);

CREATE INDEX idx_audit_log_actor ON audit_log(actor_name);

CREATE INDEX idx_audit_log_ts ON audit_log(ts);

CREATE INDEX idx_family_facts_category ON family_facts(category);

CREATE INDEX idx_family_facts_subject  ON family_facts(subject);

CREATE INDEX idx_gateway_accounts_lookup ON gateway_accounts(gateway, external_id);

CREATE INDEX idx_messages_conv ON messages(conversation_id);

CREATE INDEX idx_reminders_due_at ON reminders(due_at);

CREATE INDEX idx_reminders_user_dispatched ON reminders(user_name, dispatched);

CREATE INDEX idx_todos_user ON todos(user_name);

CREATE INDEX idx_todos_user_completed ON todos(user_name, completed);

CREATE INDEX idx_tool_audit_created   ON tool_result_audit (created_at);

CREATE INDEX idx_tool_audit_user_conv ON tool_result_audit (user_name, conv_id);

CREATE INDEX idx_tool_cache_dedup     ON tool_result_cache (user_name, tool_name, args_hash);

CREATE INDEX idx_tool_cache_expires   ON tool_result_cache (expires_at);

CREATE INDEX idx_tool_cache_user_conv ON tool_result_cache (user_name, conv_id);

CREATE INDEX idx_unknown_accounts_lookup ON unknown_accounts(gateway, external_id);

CREATE INDEX idx_used_tokens_used_at ON used_tokens(used_at);

CREATE INDEX idx_web_sessions_expires_at ON web_sessions(expires_at);

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

CREATE TABLE audit_log (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		actor_name  TEXT NOT NULL,
		gateway     TEXT NOT NULL,
		tool_name   TEXT NOT NULL,
		args        TEXT NOT NULL,
		ts          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

CREATE TABLE conversations (
		id          TEXT PRIMARY KEY,
		user_name   TEXT NOT NULL,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

CREATE TABLE family_fact_categories (
		name          TEXT PRIMARY KEY,
		description   TEXT NOT NULL,
		always_inject INTEGER NOT NULL DEFAULT 0,
		is_builtin    INTEGER NOT NULL DEFAULT 0,
		created_at    INTEGER NOT NULL,
		updated_at    INTEGER NOT NULL
	);

CREATE TABLE family_facts (
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

CREATE TABLE reminders (
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

CREATE TABLE todos (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_name  TEXT NOT NULL,
		text       TEXT NOT NULL,
		completed  INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);

CREATE TABLE tool_result_audit (
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

CREATE TABLE tool_result_cache (
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

CREATE TABLE user_role_overrides (
		user_name   TEXT PRIMARY KEY,
		role        TEXT NOT NULL,
		age_group   TEXT NOT NULL,
		set_by      TEXT NOT NULL,
		set_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

CREATE TABLE vault_secrets (
		name        TEXT PRIMARY KEY NOT NULL,
		ciphertext  BLOB NOT NULL,
		updated_at  INTEGER NOT NULL DEFAULT 0
	);

CREATE TABLE web_sessions (
		id          TEXT PRIMARY KEY NOT NULL,
		user_id     INTEGER NOT NULL,
		created_at  INTEGER NOT NULL,
		expires_at  INTEGER NOT NULL,
		last_seen   INTEGER NOT NULL,
		ip          TEXT NOT NULL DEFAULT '',
		user_agent  TEXT NOT NULL DEFAULT ''
	);

