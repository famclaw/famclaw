package family.tool_policy

import rego.v1

# Tool call policy — evaluates whether a specific tool call is allowed
# for a given user role and age group.
#
# Default DENY — explicit allow required.
# Decision is a string action (like decision.rego): "allow" | "block" |
# "request_approval". The boolean `allow` is derived from `action` so
# existing callers that only check the boolean still work.

default action := "block"
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

# Parents may call any tool.
action := "allow" if {
    input.user.role == "parent"
}

# Children may call tools not explicitly blocked for their role/age.
# file_read, file_stat, file_list are permitted for children. Only
# file_write whose content or target looks executable routes to the
# approval flow (see below) — the general allow rule excludes it via
# _needs_executable_approval so the two rule sets never overlap.
action := "allow" if {
    input.user.role == "child"
    not _child_blocked
    not _needs_executable_approval
}

# file_write with executable content: a child must get parent approval.
# The check inspects WHAT is being written (shebang, executable extension,
# binary magic), not WHO is writing it — parents bypass this entirely
# via the parent role rule above.
_needs_executable_approval if {
    input.tool_name == "file_write"
    _is_executable_write(input.args)
}

action := "request_approval" if {
    input.user.role == "child"
    _needs_executable_approval
    not approval_exists
}

action := "allow" if {
    input.user.role == "child"
    _needs_executable_approval
    approval_approved
}

action := "block" if {
    input.user.role == "child"
    _needs_executable_approval
    approval_denied
}

# allow is derived from action so the boolean query stays consistent.
allow if { action == "allow" }

# ── Child blocks (unchanged from previous policy) ────────────────────────────
# spawn_agent stays hard-blocked for children.
_child_blocked if { input.tool_name == "spawn_agent" }
# web_search is restricted to age_8_12+ (under_8 blocked).
_child_blocked if { effective_age_group == "under_8"; input.tool_name == "web_search" }
# web_fetch is restricted to age_13_17+ (under_8 and age_8_12 blocked).
_child_blocked if { effective_age_group in {"under_8", "age_8_12"}; input.tool_name == "web_fetch" }
# tool_result_more reads cached tool outputs — mirror web_fetch age restriction.
_child_blocked if { effective_age_group in {"under_8", "age_8_12"}; input.tool_name == "tool_result_more" }
# Admin tools are restricted to parent role only — never approvable.
_child_blocked if { admin_tools[input.tool_name] }

# ── Executable-content detection for file_write ─────────────────────────────
# Returns true when the file_write args look like an executable script or
# binary: a #! shebang in the content, an executable extension in the path,
# or a known binary magic header. This is a guard against a child writing
# something a human might later run, not a general content filter.

_is_executable_write(args) if {
    args != null
    _executable_path(args.path)
}
_is_executable_write(args) if {
    args != null
    _executable_content(args.content)
}

_executable_path(path) if { endswith(path, ".sh") }
_executable_path(path) if { endswith(path, ".bash") }
_executable_path(path) if { endswith(path, ".cmd") }
_executable_path(path) if { endswith(path, ".bat") }
_executable_path(path) if { endswith(path, ".exe") }
_executable_path(path) if { endswith(path, ".so") }
_executable_path(path) if { endswith(path, ".o") }

_executable_content(content) if { startswith(content, "#!") }
# ELF magic: \x7fELF
_executable_content(content) if { startswith(content, "\u007fELF") }
# Mach-O 32-bit big-endian
_executable_content(content) if { startswith(content, "\u00FE\u00ED\u00FA\u00CE") }
# Mach-O 64-bit big-endian
_executable_content(content) if { startswith(content, "\u00FE\u00ED\u00FA\u00CF") }
# Mach-O 32-bit little-endian
_executable_content(content) if { startswith(content, "\u00CE\u00FA\u00ED\u00FE") }
# Mach-O 64-bit little-endian
_executable_content(content) if { startswith(content, "\u00CF\u00FA\u00ED\u00FE") }
# Mach-O fat binary
_executable_content(content) if { startswith(content, "\u00CA\u00FE\u00BA\u00BE") }

# ── Approval helpers (mirror decision.rego shape) ─────────────────────────────

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

# ── Reasons ───────────────────────────────────────────────────────────────────

reason := "This tool is restricted to parents only." if {
    action == "block"
    admin_tools[input.tool_name]
}

reason := "This tool is not available for your age group." if {
    action == "block"
    not admin_tools[input.tool_name]
    input.tool_name != "file_write"
}

reason := "A parent denied this executable file write." if {
    action == "block"
    input.tool_name == "file_write"
}

reason := "This file looks executable and needs parent approval." if {
    action == "request_approval"
}

reason := "Allowed." if {
    action == "allow"
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
