package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupEvaluator(t *testing.T) *Evaluator {
	t.Helper()
	ev, err := NewEvaluator("", "")
	if err != nil {
		t.Fatalf("NewEvaluator with embedded policies: %v", err)
	}
	return ev
}

func TestNewEvaluator_Embedded(t *testing.T) {
	ev, err := NewEvaluator("", "")
	if err != nil {
		t.Fatalf("embedded load: %v", err)
	}
	dec, err := ev.Evaluate(context.Background(),
		makeInput("parent", "", "general", "req-embedded", nil))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if dec.Action != "allow" {
		t.Errorf("parent should be allowed, got %q (reason: %q)", dec.Action, dec.Reason)
	}
}

func TestNewEvaluator_CustomDir(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	policyDir := filepath.Join(cwd, "policies", "family")
	dataDir := filepath.Join(cwd, "policies", "data")

	ev, err := NewEvaluator(policyDir, dataDir)
	if err != nil {
		t.Fatalf("filesystem load from %q: %v", policyDir, err)
	}
	dec, err := ev.Evaluate(context.Background(),
		makeInput("child", "under_8", "self_harm", "req-customdir", nil))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if dec.Action != "block" {
		t.Errorf("child self_harm should be blocked, got %q (reason: %q)", dec.Action, dec.Reason)
	}
}

// TestNewEvaluator_HalfOverride verifies the guard against half-configured
// policy bundles. Setting only one of policyDir/dataDir would silently mix
// embedded and filesystem sources — the guard rejects this at construction.
func TestNewEvaluator_HalfOverride(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	realPolicyDir := filepath.Join(cwd, "policies", "family")
	realDataDir := filepath.Join(cwd, "policies", "data")

	tests := []struct {
		name      string
		policyDir string
		dataDir   string
		wantErr   bool
	}{
		{"both empty uses embedded", "", "", false},
		{"both set uses filesystem", realPolicyDir, realDataDir, false},
		{"only policyDir set is rejected", realPolicyDir, "", true},
		{"only dataDir set is rejected", "", realDataDir, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := NewEvaluator(tt.policyDir, tt.dataDir)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for half-override, got nil (ev=%v)", ev)
				}
				msg := err.Error()
				for _, want := range []string{"must both be set", "policyDir", "dataDir"} {
					if !strings.Contains(msg, want) {
						t.Errorf("error %q should mention %q", msg, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
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

func TestEvaluateToolCall(t *testing.T) {
	ev := setupEvaluator(t)
	ctx := context.Background()

	tests := []struct {
		name      string
		role      string
		ageGroup  string
		toolName  string
		wantAllow bool
	}{
		// default allow — unrecognised tool falls through to `default allow := true`
		{"parent unknown tool allowed", "parent", "", "some_unknown_tool", true},
		{"child unknown tool allowed", "child", "age_13_17", "some_unknown_tool", true},

		// web_search — blocked only for under_8
		{"under_8 web_search blocked", "child", "under_8", "web_search", false},
		{"age_8_12 web_search allowed", "child", "age_8_12", "web_search", true},
		{"parent web_search allowed", "parent", "", "web_search", true},

		// file_* prefix — blocked for all children, allowed for parents
		{"child file_read blocked", "child", "age_13_17", "file_read", false},
		{"child file_write blocked", "child", "age_8_12", "file_write", false},
		{"parent file_read allowed", "parent", "", "file_read", true},

		// spawn_agent — blocked for children
		{"child spawn_agent blocked", "child", "age_13_17", "spawn_agent", false},
		{"parent spawn_agent allowed", "parent", "", "spawn_agent", true},

		// web_fetch — blocked for under_8 and age_8_12
		{"under_8 web_fetch blocked", "child", "under_8", "web_fetch", false},
		{"age_8_12 web_fetch blocked", "child", "age_8_12", "web_fetch", false},
		{"age_13_17 web_fetch allowed", "child", "age_13_17", "web_fetch", true},
		{"parent web_fetch allowed", "parent", "", "web_fetch", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := ev.EvaluateToolCall(ctx, ToolCallInput{
				User:     UserInput{Role: tt.role, AgeGroup: tt.ageGroup, Name: "testuser"},
				ToolName: tt.toolName,
			})
			if err != nil {
				t.Fatalf("EvaluateToolCall error: %v", err)
			}
			if d.Allow != tt.wantAllow {
				t.Errorf("Allow = %v, want %v (reason: %q)", d.Allow, tt.wantAllow, d.Reason)
			}
			if !d.Allow && d.Reason == "" {
				t.Errorf("blocked decision should carry a reason, got empty")
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
