package family.output_policy

import rego.v1

# Output policy — evaluates LLM draft responses before delivery.
#
# Input shape:
#   {
#     "user": { "role": "parent|child", "age_group": "under_8|age_8_12|age_13_17|..." },
#     "gateway": "telegram|whatsapp|discord|web",
#     "draft_response": "<complete LLM response text>"
#   }
#
# Query path: data.family.output_policy.allow_output

default allow_output := false
default reason := "output blocked by policy"
default redact := []

# ── Age group resolution (unknown defaults to under_8 for non-parents) ─────────

effective_age_group := input.user.age_group if {
    input.user.age_group in {"under_8", "age_8_12", "age_13_17"}
}

effective_age_group := "under_8" if {
    input.user.role != "parent"
    not input.user.age_group in {"under_8", "age_8_12", "age_13_17"}
}

# ── Hard-blocked keyword sets (derived from criticalPatterns in stage_policy_output.go) ───

# self_harm / suicide keywords
_hard_blocked_keywords := {
    "suicide", "kill yourself", "self-harm", "cutting yourself",
    "pornography", "sexual intercourse", "explicit content",
    "racial slur", "ethnic cleansing", "white supremac",
    "how to make a bomb", "how to steal", "how to hack"
}

_hard_blocked if {
    some kw in _hard_blocked_keywords
    contains(lower(input.draft_response), kw)
}

# ── Soft-blocked keyword sets (age-restricted, can produce redact list) ───────

# PII patterns — blocked/redacted for all children
_pii_keywords := {"ssn", "social security"}

# Additional patterns for under_8 and age_8_12 children
_under8_keywords := {
    "violence", "blood", "murder", "weapon",
    "death", "dying", "dead body",
    "drug", "alcohol", "cigarette"
}

# Additional patterns for age_8_12 children
_age8_12_keywords := {
    "detailed violence", "graphic injury",
    "drug use", "alcohol abuse"
}

# Matched soft-blocked keywords for the current child user.
# under_8 inherits the age_8_12 keyword set so the youngest users always
# receive at least the same level of protection as older children.
_matched_soft_keywords[kw] if {
    input.user.role == "child"
    effective_age_group == "under_8"
    some kw in _under8_keywords
    contains(lower(input.draft_response), kw)
}

_matched_soft_keywords[kw] if {
    input.user.role == "child"
    effective_age_group in {"under_8", "age_8_12"}
    some kw in _age8_12_keywords
    contains(lower(input.draft_response), kw)
}

_matched_soft_keywords[kw] if {
    input.user.role == "child"
    some kw in _pii_keywords
    contains(lower(input.draft_response), kw)
}

# ── Allow rules ───────────────────────────────────────────────────────────────

# Parents: allow if no hard-blocked keyword present
allow_output := true if {
    input.user.role == "parent"
    not _hard_blocked
}

# Children: allow if no hard-blocked keyword and no soft-blocked keyword present
allow_output := true if {
    input.user.role == "child"
    not _hard_blocked
    count(_matched_soft_keywords) == 0
}

# Children: allow (with redact list) when soft-blocked keywords matched
# but no hard-blocked keyword (partial-block — caller must apply redaction)
allow_output := true if {
    input.user.role == "child"
    not _hard_blocked
    count(_matched_soft_keywords) > 0
}

# ── redact list ───────────────────────────────────────────────────────────────

redact := [kw | _matched_soft_keywords[kw]] if {
    input.user.role == "child"
    not _hard_blocked
    count(_matched_soft_keywords) > 0
}

# ── reason ────────────────────────────────────────────────────────────────────

reason := "hard-blocked content detected in response" if {
    _hard_blocked
}

reason := sprintf("response contains redactable terms: %v", [_matched_soft_keywords]) if {
    input.user.role == "child"
    not _hard_blocked
    count(_matched_soft_keywords) > 0
}

reason := "output allowed" if {
    allow_output
    count(_matched_soft_keywords) == 0
}

reason := "role not recognized" if {
    not input.user.role in {"parent", "child"}
}
