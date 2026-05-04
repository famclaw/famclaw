package family.tool_policy

import rego.v1

# Tool call policy — evaluates whether a specific tool call is allowed
# for a given user role and age group.

default allow := true

# Map unknown / missing age_group to the most restrictive bucket
# ("under_8") for non-parents. Mirrors the effective_age_group convention
# in decision.rego so a child whose age_group is empty or invalid cannot
# bypass age-restricted tool rules. The role gate below keeps parents on
# default-allow regardless.
effective_age_group := input.user.age_group if {
    input.user.age_group in {"under_8", "age_8_12", "age_13_17"}
}

effective_age_group := "under_8" if {
    not input.user.age_group in {"under_8", "age_8_12", "age_13_17"}
}

# Block web search for children whose effective age is under_8
allow := false if {
    input.user.role != "parent"
    effective_age_group == "under_8"
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

# Block web_fetch for effective-under_8 — open web content is not age-appropriate.
allow := false if {
    input.user.role != "parent"
    effective_age_group == "under_8"
    input.tool_name == "web_fetch"
}

# Block web_fetch for age_8_12 — restricted browsing requires parental supervision.
allow := false if {
    input.user.role != "parent"
    effective_age_group == "age_8_12"
    input.tool_name == "web_fetch"
}

reason := "This tool is not available for your age group." if {
    not allow
}
