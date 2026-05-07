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

reason := "This tool is not available for your age group." if {
    not allow
}
