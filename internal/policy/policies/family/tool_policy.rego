package family.tool_policy

import rego.v1

# Tool call policy — evaluates whether a specific tool call is allowed
# for a given user role and age group.
#
# Default DENY — explicit allow required.

default allow := false

# Map unknown / missing age_group to the most restrictive bucket
# ("under_8") for non-parents. Mirrors the effective_age_group convention
# in decision.rego so a child whose age_group is empty or invalid cannot
# bypass age-restricted tool rules. The role gate below keeps parents on
# explicit allow regardless.
effective_age_group := input.user.age_group if {
    input.user.age_group in {"under_8", "age_8_12", "age_13_17"}
}

effective_age_group := "under_8" if {
    not input.user.age_group in {"under_8", "age_8_12", "age_13_17"}
}

# Parents may call any tool
allow := true if {
    input.user.role == "parent"
}

# Children may call tools not explicitly blocked for their role/age
allow := true if {
    input.user.role == "child"
    not _child_blocked
}

_child_blocked if { startswith(input.tool_name, "file_") }
_child_blocked if { input.tool_name == "spawn_agent" }
_child_blocked if { effective_age_group == "under_8"; input.tool_name == "web_search" }
_child_blocked if { effective_age_group in {"under_8", "age_8_12"}; input.tool_name == "web_fetch" }
# tool_result_more reads spilled tool outputs. Cross-user ownership is
# enforced at the cache layer (ErrNotFound on mismatch), so this gate is
# defense in depth. Mirror web_fetch's age restrictions so a younger child
# can't read cached results even if they somehow obtained an id.
_child_blocked if { effective_age_group in {"under_8", "age_8_12"}; input.tool_name == "tool_result_more" }
# Admin tools are restricted to parent role only — block them for any child.
_child_blocked if { admin_tools[input.tool_name] }

reason := "This tool is restricted to parents only." if {
    not allow
    admin_tools[input.tool_name]
}

reason := "This tool is not available for your age group." if {
    not allow
    not admin_tools[input.tool_name]
}

# Admin tools are restricted to parent role only. Listed here so the
# `_child_blocked` rule and any future role gate share a single source
# of truth.
admin_tools := {
    "list_pending_approvals",
    "approve_request",
    "deny_request",
    "list_users",
    "set_user_role",
    "list_unknown_accounts",
    "link_account",
    # Phase 3.3 mutations:
    "set_family_fact",
    "delete_family_fact",
    "add_family_category",
    "delete_family_category",
    # Synthetic check fired by the propose_family_fact handler when caller is parent.
    # Closes the "OPA hole" identified by R3 council — without this, a Go bug
    # could let a child auto-apply via the propose_family_fact path.
    "family_fact_proposal_auto_apply",
}
