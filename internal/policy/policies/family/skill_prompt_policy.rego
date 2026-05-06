package family.skill_prompt_policy

import rego.v1

# Skill prompt policy — guards against prompt injection in skill inputs.
#
# Input shape (all top-level, NOT nested):
#   {
#     "skill_name": "...",
#     "prompt_body": "...",
#     "user_role": "parent|child"
#   }

default allow := false
default reason := "skill prompt blocked by policy"

# ── Injection pattern detection ───────────────────────────────────────────────

_injection_patterns := {
    "ignore previous instructions",
    "system:",
    "you are now",
    "[system]"
}

_lower_body := lower(input.prompt_body)

_injection_detected if {
    some pattern in _injection_patterns
    contains(_lower_body, pattern)
}

# ── Allow rule ────────────────────────────────────────────────────────────────

# Allow when: no injection pattern detected AND prompt is within size limit
allow := true if {
    not _injection_detected
    count(input.prompt_body) <= 2048
}

# ── Reason rules ─────────────────────────────────────────────────────────────

reason := "prompt injection pattern detected" if {
    _injection_detected
}

reason := "prompt exceeds maximum allowed length of 2048 characters" if {
    not _injection_detected
    count(input.prompt_body) > 2048
}

reason := "skill prompt allowed" if {
    allow
}
