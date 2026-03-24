package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func setupEvaluator(t *testing.T) *Evaluator {
	t.Helper()

	// Find project root by looking for go.mod
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}

	policyDir := filepath.Join(dir, "policies", "family")
	dataDir := filepath.Join(dir, "policies", "data")

	ev, err := NewEvaluator(policyDir, dataDir)
	if err != nil {
		t.Fatalf("NewEvaluator: %v", err)
	}
	return ev
}

func TestEvaluate(t *testing.T) {
	ev := setupEvaluator(t)
	ctx := context.Background()

	tests := []struct {
		name       string
		input      Input
		wantAction string
	}{
		// ── Parent always allowed ────────────────────────────────────────
		{
			name:       "parent allowed general",
			input:      makeInput("parent", "", "general", "", nil),
			wantAction: "allow",
		},
		{
			name:       "parent allowed critical",
			input:      makeInput("parent", "", "sexual_content", "", nil),
			wantAction: "allow",
		},
		{
			name:       "parent allowed high",
			input:      makeInput("parent", "", "violence", "", nil),
			wantAction: "allow",
		},

		// ── Hard-blocked (critical risk) ─────────────────────────────────
		{
			name:       "child blocked sexual_content",
			input:      makeInput("child", "age_13_17", "sexual_content", "", nil),
			wantAction: "block",
		},
		{
			name:       "child blocked self_harm",
			input:      makeInput("child", "age_8_12", "self_harm", "", nil),
			wantAction: "block",
		},
		{
			name:       "child blocked hate_speech",
			input:      makeInput("child", "under_8", "hate_speech", "", nil),
			wantAction: "block",
		},
		{
			name:       "child blocked illegal_activity",
			input:      makeInput("child", "age_13_17", "illegal_activity", "", nil),
			wantAction: "block",
		},
		{
			name:       "hard block ignores approval",
			input:      makeInput("child", "age_13_17", "sexual_content", "req-1", map[string]any{"req-1": map[string]any{"status": "approved"}}),
			wantAction: "block",
		},

		// ── under_8 ─────────────────────────────────────────────────────
		{
			name:       "under_8 allow general",
			input:      makeInput("child", "under_8", "general", "", nil),
			wantAction: "allow",
		},
		{
			name:       "under_8 allow science",
			input:      makeInput("child", "under_8", "science", "", nil),
			wantAction: "allow",
		},
		{
			name:       "under_8 block health (low)",
			input:      makeInput("child", "under_8", "health", "", nil),
			wantAction: "block",
		},
		{
			name:       "under_8 block social_media (medium)",
			input:      makeInput("child", "under_8", "social_media", "", nil),
			wantAction: "block",
		},
		{
			name:       "under_8 block violence (high)",
			input:      makeInput("child", "under_8", "violence", "", nil),
			wantAction: "block",
		},

		// ── age_8_12 ────────────────────────────────────────────────────
		{
			name:       "age_8_12 allow general",
			input:      makeInput("child", "age_8_12", "general", "", nil),
			wantAction: "allow",
		},
		{
			name:       "age_8_12 allow health (low)",
			input:      makeInput("child", "age_8_12", "health", "", nil),
			wantAction: "allow",
		},
		{
			name:       "age_8_12 request_approval social_media (medium)",
			input:      makeInput("child", "age_8_12", "social_media", "req-1", nil),
			wantAction: "request_approval",
		},
		{
			name:       "age_8_12 block violence (high)",
			input:      makeInput("child", "age_8_12", "violence", "", nil),
			wantAction: "block",
		},

		// ── age_13_17 ───────────────────────────────────────────────────
		{
			name:       "age_13_17 allow general",
			input:      makeInput("child", "age_13_17", "general", "", nil),
			wantAction: "allow",
		},
		{
			name:       "age_13_17 allow health (low)",
			input:      makeInput("child", "age_13_17", "health", "", nil),
			wantAction: "allow",
		},
		{
			name:       "age_13_17 allow social_media (medium)",
			input:      makeInput("child", "age_13_17", "social_media", "", nil),
			wantAction: "allow",
		},
		{
			name:       "age_13_17 request_approval violence (high)",
			input:      makeInput("child", "age_13_17", "violence", "req-1", nil),
			wantAction: "request_approval",
		},

		// ── Approval flow ───────────────────────────────────────────────
		{
			name:       "approval approved allows",
			input:      makeInput("child", "age_8_12", "social_media", "req-1", map[string]any{"req-1": map[string]any{"status": "approved"}}),
			wantAction: "allow",
		},
		{
			name:       "approval pending",
			input:      makeInput("child", "age_8_12", "social_media", "req-1", map[string]any{"req-1": map[string]any{"status": "pending"}}),
			wantAction: "pending",
		},
		{
			name:       "approval denied blocks",
			input:      makeInput("child", "age_8_12", "social_media", "req-1", map[string]any{"req-1": map[string]any{"status": "denied"}}),
			wantAction: "block",
		},

		// ── Unknown age_group defaults to under_8 ───────────────────────
		{
			name:       "unknown age allows general",
			input:      makeInput("child", "", "general", "", nil),
			wantAction: "allow",
		},
		{
			name:       "unknown age blocks health",
			input:      makeInput("child", "", "health", "", nil),
			wantAction: "block",
		},
		{
			name:       "bogus age blocks medium",
			input:      makeInput("child", "toddler", "social_media", "", nil),
			wantAction: "block",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := ev.Evaluate(ctx, tt.input)
			if err != nil {
				t.Fatalf("Evaluate error: %v", err)
			}
			if d.Action != tt.wantAction {
				t.Errorf("action = %q, want %q (reason: %q)", d.Action, tt.wantAction, d.Reason)
			}
		})
	}
}

func makeInput(role, ageGroup, category, requestID string, approvals map[string]any) Input {
	if approvals == nil {
		approvals = map[string]any{}
	}
	return Input{
		User: UserInput{
			Role:     role,
			AgeGroup: ageGroup,
			Name:     "testuser",
		},
		Query: QueryInput{
			Category: category,
			Text:     "test query",
		},
		RequestID: requestID,
		Approvals: approvals,
	}
}
