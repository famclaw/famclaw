package family.tool_policy

import rego.v1

# Tool call policy — evaluates whether a specific tool call is allowed
# for a given user role and age group.

default allow := true

# Block web search for children under 8
allow := false if {
    input.user.age_group == "under_8"
    input.tool_name == "web_search"
}

# Block file system tools for all children
allow := false if {
    input.user.role == "child"
    startswith(input.tool_name, "file_")
}

# Block spawn_agent for children
allow := false if {
    input.user.role == "child"
    input.tool_name == "spawn_agent"
}

reason := "This tool is not available for your age group." if {
    not allow
}
