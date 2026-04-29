package family.decision

import rego.v1

# Default deny — explicit allow required
default action := "block"
default reason := "No matching policy rule"

# ── Parents always allowed ────────────────────────────────────────────────────

action := "allow" if {
	input.user.role == "parent"
}

reason := "Parent role — always allowed" if {
	input.user.role == "parent"
}

# ── Hard-blocked categories (critical risk) — no override possible ────────────

hard_blocked := {"sexual_content", "self_harm", "hate_speech", "illegal_activity"}

action := "block" if {
	input.user.role != "parent"
	hard_blocked[input.query.category]
}

reason := sprintf("Category %q is permanently blocked", [input.query.category]) if {
	input.user.role != "parent"
	hard_blocked[input.query.category]
}

# ── Risk lookup from data ─────────────────────────────────────────────────────

category_risk := data.categories[input.query.category].risk

# ── Age group resolution (unknown defaults to under_8) ────────────────────────

effective_age_group := input.user.age_group if {
	input.user.age_group in {"under_8", "age_8_12", "age_13_17"}
}

effective_age_group := "under_8" if {
	not input.user.age_group in {"under_8", "age_8_12", "age_13_17"}
}

# ── under_8: only "none" risk allowed ────────────────────────────────────────

action := "allow" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "under_8"
	category_risk == "none"
}

reason := "Safe category for young children" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "under_8"
	category_risk == "none"
}

action := "block" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "under_8"
	category_risk in {"low", "medium", "high"}
}

reason := sprintf("Category %q is not available for young children", [input.query.category]) if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "under_8"
	category_risk in {"low", "medium", "high"}
}

# ── age_8_12: "none" + "low" allowed, "medium" needs approval, "high" blocked ─

action := "allow" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_8_12"
	category_risk in {"none", "low"}
}

reason := "Safe category for this age group" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_8_12"
	category_risk in {"none", "low"}
}

action := "block" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_8_12"
	category_risk == "high"
}

reason := sprintf("Category %q is blocked for ages 8-12", [input.query.category]) if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_8_12"
	category_risk == "high"
}

# age_8_12 medium risk → approval flow
action := "request_approval" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_8_12"
	category_risk == "medium"
	not approval_exists
}

reason := "This topic requires parental approval" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_8_12"
	category_risk == "medium"
	not approval_exists
}

action := "allow" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_8_12"
	category_risk == "medium"
	approval_approved
}

reason := "Parent approved this topic" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_8_12"
	category_risk == "medium"
	approval_approved
}

action := "pending" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_8_12"
	category_risk == "medium"
	approval_pending
}

reason := "Waiting for parent to decide" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_8_12"
	category_risk == "medium"
	approval_pending
}

action := "block" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_8_12"
	category_risk == "medium"
	approval_denied
}

reason := "Parent denied this topic" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_8_12"
	category_risk == "medium"
	approval_denied
}

# ── age_13_17: "none"+"low"+"medium" allowed, "high" needs approval ──────────

action := "allow" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_13_17"
	category_risk in {"none", "low", "medium"}
}

reason := "Safe category for teenagers" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_13_17"
	category_risk in {"none", "low", "medium"}
}

# age_13_17 high risk → approval flow
action := "request_approval" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_13_17"
	category_risk == "high"
	not approval_exists
}

reason := "This topic requires parental approval for teenagers" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_13_17"
	category_risk == "high"
	not approval_exists
}

action := "allow" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_13_17"
	category_risk == "high"
	approval_approved
}

reason := "Parent approved this topic" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_13_17"
	category_risk == "high"
	approval_approved
}

action := "pending" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_13_17"
	category_risk == "high"
	approval_pending
}

reason := "Waiting for parent to decide" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_13_17"
	category_risk == "high"
	approval_pending
}

action := "block" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_13_17"
	category_risk == "high"
	approval_denied
}

reason := "Parent denied this topic" if {
	input.user.role != "parent"
	not hard_blocked[input.query.category]
	effective_age_group == "age_13_17"
	category_risk == "high"
	approval_denied
}

# ── Approval helpers ──────────────────────────────────────────────────────────

approval_exists if {
	input.approvals[input.request_id]
}

approval_approved if {
	input.approvals[input.request_id].status == "approved"
}

approval_pending if {
	input.approvals[input.request_id].status == "pending"
}

approval_denied if {
	input.approvals[input.request_id].status == "denied"
}
