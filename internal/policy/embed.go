package policy

import "embed"

// Default policy modules compiled into the binary. Explicit file lists
// keep *_test.rego out of production loads.

//go:embed policies/family/decision.rego policies/family/tool_policy.rego policies/family/output_policy.rego policies/family/skill_prompt_policy.rego
var embeddedPolicies embed.FS

//go:embed policies/data/topics.json
var embeddedData embed.FS
