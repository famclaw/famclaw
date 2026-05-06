package family.output_policy_test

import rego.v1
import data.family.output_policy

# 1. Parent with benign text → allow, no redactions
test_parent_benign if {
    output_policy.allow_output with input as {
        "user": {"role": "parent", "age_group": ""},
        "gateway": "web",
        "draft_response": "The sky is blue because of Rayleigh scattering."
    }
    output_policy.redact == [] with input as {
        "user": {"role": "parent", "age_group": ""},
        "gateway": "web",
        "draft_response": "The sky is blue because of Rayleigh scattering."
    }
}

# 2. Parent, hard-blocked keyword "suicide" → deny
test_parent_hard_blocked if {
    not output_policy.allow_output with input as {
        "user": {"role": "parent", "age_group": ""},
        "gateway": "web",
        "draft_response": "One option is suicide if life feels too hard."
    }
}

# 3. Child age_13_17 with benign text → allow
test_child_benign if {
    output_policy.allow_output with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "gateway": "web",
        "draft_response": "Photosynthesis converts sunlight into energy for plants."
    }
}

# 4. Child age_8_12 with soft-blocked term → allow_output true, redact non-empty
test_child_soft_blocked_redact if {
    output_policy.allow_output with input as {
        "user": {"role": "child", "age_group": "age_8_12"},
        "gateway": "web",
        "draft_response": "The story describes detailed violence in the battle scene."
    }
    count(output_policy.redact) > 0 with input as {
        "user": {"role": "child", "age_group": "age_8_12"},
        "gateway": "web",
        "draft_response": "The story describes detailed violence in the battle scene."
    }
}

# 5. Child with PII (SSN) in response → blocked or redacted
#    "your SSN is 123-45-6789" must be caught by PII detection
test_child_pii_leak if {
    # Either blocked entirely or redact list is non-empty
    count(output_policy.redact) > 0 with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "gateway": "web",
        "draft_response": "your SSN is 123-45-6789 as requested."
    }
}

# 6. Unknown role → deny
test_unknown_role if {
    not output_policy.allow_output with input as {
        "user": {"role": "unknown", "age_group": "age_13_17"},
        "gateway": "web",
        "draft_response": "The sky is blue because of Rayleigh scattering."
    }
}
