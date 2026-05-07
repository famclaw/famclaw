package family.tool_policy_test

import rego.v1
import data.family.tool_policy

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

# Under 8 cannot use web search
test_under8_no_web_search if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "under_8"},
        "tool_name": "web_search"
    }
}

# Children cannot use file tools
test_child_no_file_tools if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_8_12"},
        "tool_name": "file_read"
    }
}

# Children cannot spawn agents
test_child_no_spawn if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "spawn_agent"
    }
}

# Teenager can use calculator
test_teen_calculator if {
    tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "calculator"
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

# New tests for default-DENY migration

# Parent explicitly allowed to call spawn_agent
test_parent_explicit_allow_spawn_agent if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "spawn_agent"
    }
}

# Child calling an arbitrary tool not in any block list → allowed
test_unknown_tool_child_allowed if {
    tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "calculator_v2"
    }
}

# Unknown role (not "parent" or "child") → denied under default-DENY
test_unknown_tool_blocked_for_unknown_role if {
    not tool_policy.allow with input as {
        "user": {"role": "guest", "age_group": "age_13_17"},
        "tool_name": "calculator_v2"
    }
}

# --- Admin tools: only parents may use them ---

# list_pending_approvals
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

# approve_request
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

# deny_request
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

# list_users
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

# set_user_role
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

# list_unknown_accounts
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

# link_account
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
