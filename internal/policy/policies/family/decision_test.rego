package family.decision_test

import rego.v1

import data.family.decision

# ── Helper: build input ──────────────────────────────────────────────────────

mock_input(role, age_group, category) := {
	"user": {"role": role, "age_group": age_group, "name": "test"},
	"query": {"category": category, "text": "test query"},
	"request_id": "req-1",
	"approvals": {},
}

mock_input_with_approval(role, age_group, category, status) := {
	"user": {"role": role, "age_group": age_group, "name": "test"},
	"query": {"category": category, "text": "test query"},
	"request_id": "req-1",
	"approvals": {"req-1": {"status": status}},
}

# ── 1. Parent always allowed ─────────────────────────────────────────────────

test_parent_allowed_general if {
	decision.action == "allow" with input as mock_input("parent", "", "general")
}

test_parent_allowed_critical if {
	decision.action == "allow" with input as mock_input("parent", "", "sexual_content")
}

test_parent_allowed_high if {
	decision.action == "allow" with input as mock_input("parent", "", "violence")
}

# ── 2. Hard-blocked categories ───────────────────────────────────────────────

test_child_blocked_sexual_content if {
	decision.action == "block" with input as mock_input("child", "age_13_17", "sexual_content")
}

test_child_blocked_self_harm if {
	decision.action == "block" with input as mock_input("child", "age_8_12", "self_harm")
}

test_child_blocked_hate_speech if {
	decision.action == "block" with input as mock_input("child", "under_8", "hate_speech")
}

test_child_blocked_illegal if {
	decision.action == "block" with input as mock_input("child", "age_13_17", "illegal_activity")
}

# Hard-blocked even with approval
test_hard_blocked_with_approval if {
	decision.action == "block" with input as mock_input_with_approval("child", "age_13_17", "sexual_content", "approved")
}

# ── 3. under_8 rules ─────────────────────────────────────────────────────────

test_under8_allow_general if {
	decision.action == "allow" with input as mock_input("child", "under_8", "general")
}

test_under8_allow_science if {
	decision.action == "allow" with input as mock_input("child", "under_8", "science")
}

test_under8_block_health if {
	decision.action == "block" with input as mock_input("child", "under_8", "health")
}

test_under8_block_social_media if {
	decision.action == "block" with input as mock_input("child", "under_8", "social_media")
}

test_under8_block_violence if {
	decision.action == "block" with input as mock_input("child", "under_8", "violence")
}

# ── 4. age_8_12 rules ────────────────────────────────────────────────────────

test_8_12_allow_general if {
	decision.action == "allow" with input as mock_input("child", "age_8_12", "general")
}

test_8_12_allow_health if {
	decision.action == "allow" with input as mock_input("child", "age_8_12", "health")
}

test_8_12_request_approval_social_media if {
	decision.action == "request_approval" with input as mock_input("child", "age_8_12", "social_media")
}

test_8_12_block_violence if {
	decision.action == "block" with input as mock_input("child", "age_8_12", "violence")
}

# ── 5. age_13_17 rules ───────────────────────────────────────────────────────

test_13_17_allow_general if {
	decision.action == "allow" with input as mock_input("child", "age_13_17", "general")
}

test_13_17_allow_health if {
	decision.action == "allow" with input as mock_input("child", "age_13_17", "health")
}

test_13_17_allow_social_media if {
	decision.action == "allow" with input as mock_input("child", "age_13_17", "social_media")
}

test_13_17_request_approval_violence if {
	decision.action == "request_approval" with input as mock_input("child", "age_13_17", "violence")
}

# ── 6. Approval flow ─────────────────────────────────────────────────────────

test_approval_approved if {
	decision.action == "allow" with input as mock_input_with_approval("child", "age_8_12", "social_media", "approved")
}

test_approval_pending if {
	decision.action == "pending" with input as mock_input_with_approval("child", "age_8_12", "social_media", "pending")
}

test_approval_denied if {
	decision.action == "block" with input as mock_input_with_approval("child", "age_8_12", "social_media", "denied")
}

# ── 7. Unknown age_group defaults to under_8 ─────────────────────────────────

test_unknown_age_defaults_to_under8 if {
	decision.action == "block" with input as mock_input("child", "", "health")
}

test_unknown_age_allows_general if {
	decision.action == "allow" with input as mock_input("child", "", "general")
}

test_bogus_age_defaults_to_under8 if {
	decision.action == "block" with input as mock_input("child", "toddler", "social_media")
}
