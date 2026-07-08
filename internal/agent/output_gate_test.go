package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/policy"
)

func TestEvaluateAndApply(t *testing.T) {
	eval, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatalf("NewEvaluator: %v", err)
	}

	tests := []struct {
		name        string
		draft       string
		user        policy.UserInput
		gateway     string
		wantAllowed bool
		wantEmpty   bool   // expect empty string returned
		wantContain string // substring that must appear in result (if non-empty)
		wantAbsent  string // substring that must NOT appear in result (if non-empty)
	}{
		{
			name:        "benign child response is allowed unchanged",
			draft:       "The sun is a star at the center of our solar system.",
			user:        policy.UserInput{Role: "child", AgeGroup: "age_8_12"},
			gateway:     "web",
			wantAllowed: true,
			wantContain: "The sun is a star",
		},
		{
			name:        "hard-blocked response with suicide is denied",
			draft:       "Here is some advice: suicide is an option if you feel hopeless.",
			user:        policy.UserInput{Role: "child", AgeGroup: "age_13_17"},
			gateway:     "telegram",
			wantAllowed: false,
			wantEmpty:   true,
		},
		{
			name:        "soft-blocked PII for age_8_12 is redacted",
			draft:       "Your ssn is 123-45-6789 and you should protect it.",
			user:        policy.UserInput{Role: "child", AgeGroup: "age_8_12"},
			gateway:     "web",
			wantAllowed: true,
			wantContain: "[redacted]",
			wantAbsent:  "ssn", // the keyword itself must be replaced
		},
		{
			name:        "parent with benign content is allowed",
			draft:       "Here is your financial report for the month.",
			user:        policy.UserInput{Role: "parent", AgeGroup: ""},
			gateway:     "discord",
			wantAllowed: true,
			wantContain: "financial report",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			final, allowed, err := EvaluateAndApply(context.Background(), eval, tt.draft, tt.user, tt.gateway)
			if err != nil {
				t.Fatalf("EvaluateAndApply error: %v", err)
			}
			if allowed != tt.wantAllowed {
				t.Errorf("allowed = %v, want %v", allowed, tt.wantAllowed)
			}
			if tt.wantEmpty && final != "" {
				t.Errorf("expected empty final, got %q", final)
			}
			if tt.wantContain != "" && !strings.Contains(final, tt.wantContain) {
				t.Errorf("final %q does not contain %q", final, tt.wantContain)
			}
			if tt.wantAbsent != "" && strings.Contains(final, tt.wantAbsent) {
				t.Errorf("final %q should not contain %q", final, tt.wantAbsent)
			}
		})
	}
}
