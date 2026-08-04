package family.tool_policy_test

import rego.v1

import data.family.tool_policy

# ── Helpers ───────────────────────────────────────────────────────────────────

# Build a minimal input for a child tool call.
child_input(tool_name, args) := {
    "user": {"role": "child", "age_group": "age_8_12", "name": "kid"},
    "tool_name": tool_name,
    "args": args,
    "request_id": "",
    "approvals": null,
}

child_input_no_args(tool_name) := {
    "user": {"role": "child", "age_group": "age_8_12", "name": "kid"},
    "tool_name": tool_name,
    "args": null,
    "request_id": "",
    "approvals": null,
}

parent_input(tool_name) := {
    "user": {"role": "parent", "age_group": "", "name": "mom"},
    "tool_name": tool_name,
}

# ── 1. Parents always allowed ────────────────────────────────────────────────

# Parent can use any tool
test_parent_web_search if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "web_search"
    }
}

test_parent_spawn_agent if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "spawn_agent"
    }
}

test_parent_file_read if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "file_read"
    }
}

test_parent_file_write if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "file_write",
        "args": {"path": "script.sh", "content": "#!/bin/bash\necho hi"}
    }
}

# Parent file_write with executable content is still allowed (no approval needed)
test_parent_executable_file_write_allowed if {
    tool_policy.allow with input as parent_input("file_write") with input.args as {"path": "run.sh", "content": "#!/bin/bash\necho hi"}
}

# ── 2. Children can use non-executable file tools ────────────────────────────

# file_read, file_stat, file_list are allowed for children
test_child_file_read_allowed if {
    tool_policy.allow with input as child_input_no_args("file_read")
}

test_child_file_stat_allowed if {
    tool_policy.allow with input as child_input_no_args("file_stat")
}

test_child_file_list_allowed if {
    tool_policy.allow with input as child_input_no_args("file_list")
}

# file_write with non-executable content is allowed for children
test_child_file_write_allowed if {
    tool_policy.allow with input as child_input_no_args("file_write") with input.args as {"path": "notes.txt", "content": "hello world"}
}

test_child_file_write_no_extension_allowed if {
    tool_policy.allow with input as child_input_no_args("file_write") with input.args as {"path": "notes", "content": "hello world"}
}

test_child_file_write_json_allowed if {
    tool_policy.allow with input as child_input_no_args("file_write") with input.args as {"path": "data.json", "content": "{\"key\": \"value\"}"}
}

# ── 3. file_write with executable content → request_approval for children ───

# Shebang in content
test_child_executable_shebang_requests_approval if {
    tool_policy.action == "request_approval" with input as child_input_no_args("file_write") with input.args as {"path": "run.txt", "content": "#!/bin/bash\necho hi"}
}

# Executable extension
test_child_executable_sh_requests_approval if {
    tool_policy.action == "request_approval" with input as child_input_no_args("file_write") with input.args as {"path": "run.sh", "content": "echo hi"}
}

test_child_executable_bash_requests_approval if {
    tool_policy.action == "request_approval" with input as child_input_no_args("file_write") with input.args as {"path": "run.bash", "content": "echo hi"}
}

test_child_executable_exe_requests_approval if {
    tool_policy.action == "request_approval" with input as child_input_no_args("file_write") with input.args as {"path": "run.exe", "content": "echo hi"}
}

test_child_executable_so_requests_approval if {
    tool_policy.action == "request_approval" with input as child_input_no_args("file_write") with input.args as {"path": "lib.so", "content": "echo hi"}
}

# ELF magic bytes
test_child_executable_elf_requests_approval if {
    tool_policy.action == "request_approval" with input as child_input_no_args("file_write") with input.args as {"path": "binary", "content": "\u007fELF"}
}

# When approval is requested, allow is false (tool does NOT execute)
test_child_executable_shebang_not_allowed if {
    not tool_policy.allow with input as child_input_no_args("file_write") with input.args as {"path": "run.txt", "content": "#!/bin/bash\necho hi"}
}

# Reason is meaningful
test_child_executable_reason if {
    tool_policy.reason == "This file looks executable and needs parent approval." with input as child_input_no_args("file_write") with input.args as {"path": "run.sh", "content": "echo hi"}
}

# ── 4. file_write executable with existing approval ─────────────────────────

# Already approved → allow
test_child_executable_approved if {
    tool_policy.action == "allow" with input as {
        "user": {"role": "child", "age_group": "age_8_12", "name": "kid"},
        "tool_name": "file_write",
        "args": {"path": "run.sh", "content": "echo hi"},
        "request_id": "req-1",
        "approvals": {"req-1": {"status": "approved", "decided_by": "mom"}},
    }
}

# Already pending → not request_approval (stays request_approval is wrong;
# pending means the approval exists and is waiting)
test_child_executable_pending_not_again if {
    not tool_policy.action == "request_approval" with input as {
        "user": {"role": "child", "age_group": "age_8_12", "name": "kid"},
        "tool_name": "file_write",
        "args": {"path": "run.sh", "content": "echo hi"},
        "request_id": "req-1",
        "approvals": {"req-1": {"status": "pending"}},
    }
}

# Already denied → block
test_child_executable_denied_blocks if {
    tool_policy.action == "block" with input as {
        "user": {"role": "child", "age_group": "age_8_12", "name": "kid"},
        "tool_name": "file_write",
        "args": {"path": "run.sh", "content": "echo hi"},
        "request_id": "req-1",
        "approvals": {"req-1": {"status": "denied", "decided_by": "mom"}},
    }
}

# When denied, allow is false
test_child_executable_denied_not_allowed if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_8_12", "name": "kid"},
        "tool_name": "file_write",
        "args": {"path": "run.sh", "content": "echo hi"},
        "request_id": "req-1",
        "approvals": {"req-1": {"status": "denied"}},
    }
}

# ── 5. spawn_agent and web tools stay blocked for children ──────────────────

# Under 8 cannot use web search
test_under8_no_web_search if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "under_8"},
        "tool_name": "web_search"
    }
}

# Children cannot spawn agents
test_child_no_spawn if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "spawn_agent"
    }
}

# web_fetch policy
test_parent_web_fetch if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "web_fetch"
    }
}

test_teen_web_fetch if {
    tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "web_fetch"
    }
}

test_under8_no_web_fetch if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "under_8"},
        "tool_name": "web_fetch"
    }
}

test_age8_12_no_web_fetch if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_8_12"},
        "tool_name": "web_fetch"
    }
}

# Unknown / bogus / empty age_group on a child must collapse to under_8
# rules — no bypass via missing or unrecognized age_group.
test_unknown_age_no_web_fetch if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": ""},
        "tool_name": "web_fetch"
    }
}

test_bogus_age_no_web_fetch if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "toddler"},
        "tool_name": "web_fetch"
    }
}

test_unknown_age_no_web_search if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": ""},
        "tool_name": "web_search"
    }
}

# After narrowing the web_search block to effective-under_8 only,
# age_8_12 and age_13_17 children must be able to use web_search.
test_age8_12_web_search_allowed if {
    tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_8_12"},
        "tool_name": "web_search"
    }
}

test_teen_web_search_allowed if {
    tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "web_search"
    }
}

# Parent with empty age_group must NOT fall back to under_8 — parents
# bypass the age-fallback gates entirely.
test_parent_empty_age_still_allowed if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "web_fetch"
    }
}

# Teenager can use calculator
test_teen_calculator if {
    tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "calculator"
    }
}

# ── 6. Admin tools: only parents may use them ──────────────────────────────

test_parent_can_list_pending_approvals if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "list_pending_approvals"
    }
}

test_child_cannot_list_pending_approvals if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_8_12"},
        "tool_name": "list_pending_approvals"
    }
}

test_parent_can_approve_request if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "approve_request"
    }
}

test_child_cannot_approve_request if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "approve_request"
    }
}

test_parent_can_deny_request if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "deny_request"
    }
}

test_child_cannot_deny_request if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "under_8"},
        "tool_name": "deny_request"
    }
}

test_parent_can_list_users if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "list_users"
    }
}

test_child_cannot_list_users if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_8_12"},
        "tool_name": "list_users"
    }
}

test_parent_can_set_user_role if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "set_user_role"
    }
}

test_child_cannot_set_user_role if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "set_user_role"
    }
}

test_parent_can_list_unknown_accounts if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "list_unknown_accounts"
    }
}

test_child_cannot_list_unknown_accounts if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "under_8"},
        "tool_name": "list_unknown_accounts"
    }
}

test_parent_can_link_account if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "link_account"
    }
}

test_child_cannot_link_account if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_8_12"},
        "tool_name": "link_account"
    }
}

# tool_result_more
test_parent_tool_result_more if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "tool_result_more"
    }
}

test_teen_tool_result_more if {
    tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "tool_result_more"
    }
}

test_under8_no_tool_result_more if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "under_8"},
        "tool_name": "tool_result_more"
    }
}

test_age8_12_no_tool_result_more if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_8_12"},
        "tool_name": "tool_result_more"
    }
}

# ── 7. Phase 3.3 family_state tests ─────────────────────────────────────────

test_parent_set_family_fact if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "set_family_fact"
    }
}

test_child_no_set_family_fact if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "set_family_fact"
    }
}

test_parent_delete_family_fact if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "delete_family_fact"
    }
}

test_child_no_delete_family_fact if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_8_12"},
        "tool_name": "delete_family_fact"
    }
}

test_parent_add_family_category if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "add_family_category"
    }
}

test_child_no_add_family_category if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "under_8"},
        "tool_name": "add_family_category"
    }
}

test_parent_delete_family_category if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "delete_family_category"
    }
}

test_child_no_delete_family_category if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "delete_family_category"
    }
}

# get_family_state and propose_family_fact must be callable by all roles.

test_parent_get_family_state if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "get_family_state"
    }
}

test_child_get_family_state if {
    tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_8_12"},
        "tool_name": "get_family_state"
    }
}

test_parent_propose_family_fact if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "propose_family_fact"
    }
}

test_child_propose_family_fact if {
    tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "propose_family_fact"
    }
}

# OPA hole closure: synthetic auto-apply name is parent-only.

test_parent_auto_apply_allowed if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "family_fact_proposal_auto_apply"
    }
}

test_child_auto_apply_denied if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "family_fact_proposal_auto_apply"
    }
}

# ── 8. Executable file_write edge cases ─────────────────────────────────────

# No args (nil) — should be allowed (no content to check)
test_child_file_write_nil_args_allowed if {
    tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_8_12", "name": "kid"},
        "tool_name": "file_write"
    }
}

# Empty content, safe extension — allowed
test_child_file_write_empty_content_allowed if {
    tool_policy.allow with input as child_input_no_args("file_write") with input.args as {"path": "empty.txt", "content": ""}
}

# Content with shebang but safe extension — still needs approval
test_child_file_write_shebang_safe_ext_requests_approval if {
    tool_policy.action == "request_approval" with input as child_input_no_args("file_write") with input.args as {"path": "notes.txt", "content": "#! /bin/bash\necho hi"}
}

# Under 8 child with executable file_write — also needs approval
test_under8_executable_requests_approval if {
    tool_policy.action == "request_approval" with input as {
        "user": {"role": "child", "age_group": "under_8", "name": "kid"},
        "tool_name": "file_write",
        "args": {"path": "run.sh", "content": "echo hi"}
    }
}

# file_read is never caught by executable detection
test_child_file_read_not_caught_by_executable if {
    tool_policy.allow with input as child_input_no_args("file_read") with input.args as {"path": "run.sh"}
}

# spawn_agent for child is blocked (not request_approval)
test_child_spawn_blocked_not_approval if {
    not tool_policy.action == "request_approval" with input as child_input_no_args("spawn_agent")
}

# Admin tool for child is blocked (not request_approval)
test_child_admin_blocked_not_approval if {
    not tool_policy.action == "request_approval" with input as child_input_no_args("list_users")
}

# ── 9. add_reminder (set a reminder for self or family member) ──────────────

# add_reminder is allowed for all roles at the policy level; the cross-user
# (for_user) gate is enforced in the Go handler, not in OPA.

test_parent_add_reminder if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "add_reminder"
    }
}

test_child_add_reminder if {
    tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "add_reminder"
    }
}

test_under8_add_reminder if {
    tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "under_8"},
        "tool_name": "add_reminder"
    }
}
