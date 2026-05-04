-- Fake seed data for FamClaw integration tests.
-- All names, IDs, and tokens are placeholder values. NO real PII.

INSERT INTO conversations (id, user_name, created_at, updated_at) VALUES
    ('conv-test-1', 'parent_alpha', '2026-01-01 00:00:00', '2026-01-01 00:00:00');

INSERT INTO messages (conversation_id, role, content, category, policy_action, created_at) VALUES
    ('conv-test-1', 'user',      'Hello test',       'general', 'allow', '2026-01-01 00:00:01'),
    ('conv-test-1', 'assistant', 'Hi from FamClaw',  'general', 'allow', '2026-01-01 00:00:02');

INSERT INTO gateway_accounts (user_name, gateway, external_id) VALUES
    ('parent_alpha', 'telegram', 'tg-test-1001'),
    ('child_beta',   'discord',  'dc-test-snowflake-1');

INSERT INTO unknown_accounts (gateway, external_id, display_name) VALUES
    ('telegram', 'tg-test-9999', 'Test Stranger');

INSERT INTO approvals (id, user_name, user_display, age_group, category, query_text, status, expires_at) VALUES
    ('appr-test-1', 'child_beta', 'Child Beta', 'age_8_12', 'general', 'test approval question', 'pending', '2026-01-02 00:00:00');

INSERT INTO installed_skills (name, repo_url, version, version_sha) VALUES
    ('test-skill', 'https://example.invalid/test-skill', 'v0.0.1-test', 'fake-sha-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');
