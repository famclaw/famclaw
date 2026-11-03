//go:build e2e || integration

package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/agent/tools/admin"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// assertAuditLogged asserts that the most recent audit_log row for toolName
// has the expected actor and that every entry in argsContains appears in the
// stored args JSON. Guards against silent regressions where a mutating tool
// stops writing its audit entry.
func assertAuditLogged(t *testing.T, db *store.DB, toolName, actor string, argsContains ...string) {
	t.Helper()
	rows, err := db.ListAuditLogs(context.Background(), 20)
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	var match *store.AuditLog
	for _, r := range rows {
		if r.ToolName == toolName {
			match = r
			break
		}
	}
	if match == nil {
		t.Fatalf("no audit_log row for tool %q (recent rows: %d)", toolName, len(rows))
	}
	if match.ActorName != actor {
		t.Errorf("audit row actor = %q, want %q", match.ActorName, actor)
	}
	if match.Ts.IsZero() {
		t.Errorf("audit row Ts is zero")
	}
	for _, sub := range argsContains {
		if !strings.Contains(match.Args, sub) {
			t.Errorf("audit row args missing %q (got %q)", sub, match.Args)
		}
	}
}

func newAdminTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "admin-test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func testConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Host:   "localhost",
			Port:   8080,
			Secret: "test-secret",
		},
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent"},
			{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"},
		},
	}
}

func parentDeps(db *store.DB) admin.Deps {
	return admin.Deps{
		DB:      db,
		Cfg:     testConfig(),
		Actor:   "parent",
		Gateway: "web",
	}
}

// insertPendingApproval inserts a pending approval row and returns its ID.
func insertPendingApproval(t *testing.T, db *store.DB, userName, category string) string {
	t.Helper()
	id := userName + "-" + category + "-" + time.Now().UTC().Format("20060102150405")
	a := &store.Approval{
		ID:          id,
		UserName:    userName,
		UserDisplay: "Emma",
		AgeGroup:    "age_8_12",
		Category:    category,
		QueryText:   "test query for " + category,
	}
	isNew, err := db.UpsertApproval(a)
	if err != nil {
		t.Fatalf("UpsertApproval: %v", err)
	}
	if !isNew {
		t.Fatalf("approval %s already existed", id)
	}
	return id
}

// insertUnknownAccount inserts an unknown account row and returns its ID.
func insertUnknownAccount(t *testing.T, db *store.DB, gateway, externalID, displayName string) int64 {
	t.Helper()
	ctx := context.Background()
	if err := db.RecordUnknownAccount(ctx, gateway, externalID, displayName); err != nil {
		t.Fatalf("RecordUnknownAccount: %v", err)
	}
	accounts, err := db.ListUnknownAccounts(ctx)
	if err != nil {
		t.Fatalf("ListUnknownAccounts after insert: %v", err)
	}
	for _, a := range accounts {
		if a.Gateway == gateway && a.ExternalID == externalID {
			return a.ID
		}
	}
	t.Fatalf("inserted unknown account not found: %s/%s", gateway, externalID)
	return 0
}

// ── Happy-path tests (parent can use the tool) ────────────────────────────────

func TestAdminListPendingApprovals_Parent(t *testing.T) {
	db := newAdminTestDB(t)
	ctx := context.Background()

	// Insert a pending approval so the result is non-empty.
	insertPendingApproval(t, db, "emma", "social_media")

	result, err := admin.HandleListPendingApprovals(ctx, parentDeps(db), map[string]any{})
	if err != nil {
		t.Fatalf("HandleListPendingApprovals: %v", err)
	}

	var approvals []map[string]any
	if err := json.Unmarshal([]byte(result), &approvals); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(approvals) < 1 {
		t.Fatalf("expected at least 1 pending approval, got %d", len(approvals))
	}
	first := approvals[0]
	if first["user_name"] != "emma" {
		t.Errorf("user_name = %v, want 'emma'", first["user_name"])
	}
	if first["category"] != "social_media" {
		t.Errorf("category = %v, want 'social_media'", first["category"])
	}
	if _, ok := first["id"]; !ok {
		t.Error("result missing 'id' field")
	}
}

func TestAdminApproveRequest_Parent(t *testing.T) {
	db := newAdminTestDB(t)
	ctx := context.Background()

	id := insertPendingApproval(t, db, "emma", "health")

	result, err := admin.HandleApproveRequest(ctx, parentDeps(db), map[string]any{
		"request_id": id,
	})
	if err != nil {
		t.Fatalf("HandleApproveRequest: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if resp["status"] != "approved" {
		t.Errorf("status = %v, want 'approved'", resp["status"])
	}
	if resp["decided_by"] != "parent" {
		t.Errorf("decided_by = %v, want 'parent'", resp["decided_by"])
	}

	// Verify in DB.
	a, err := db.GetApproval(ctx, id)
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if a == nil {
		t.Fatal("approval not found in DB after approve")
	}
	if a.Status != "approved" {
		t.Errorf("DB status = %q, want 'approved'", a.Status)
	}

	assertAuditLogged(t, db, "builtin__approve_request", "parent", id)
}

func TestAdminDenyRequest_Parent(t *testing.T) {
	db := newAdminTestDB(t)
	ctx := context.Background()

	id := insertPendingApproval(t, db, "emma", "violence")

	result, err := admin.HandleDenyRequest(ctx, parentDeps(db), map[string]any{
		"request_id": id,
		"reason":     "not appropriate",
	})
	if err != nil {
		t.Fatalf("HandleDenyRequest: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if resp["status"] != "denied" {
		t.Errorf("status = %v, want 'denied'", resp["status"])
	}
	if resp["reason"] != "not appropriate" {
		t.Errorf("reason = %v, want 'not appropriate'", resp["reason"])
	}

	// Verify in DB.
	a, err := db.GetApproval(ctx, id)
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if a == nil {
		t.Fatal("approval not found in DB after deny")
	}
	if a.Status != "denied" {
		t.Errorf("DB status = %q, want 'denied'", a.Status)
	}

	assertAuditLogged(t, db, "builtin__deny_request", "parent", id, "not appropriate")
}

func TestAdminListUsers_Parent(t *testing.T) {
	db := newAdminTestDB(t)
	ctx := context.Background()

	result, err := admin.HandleListUsers(ctx, parentDeps(db), map[string]any{})
	if err != nil {
		t.Fatalf("HandleListUsers: %v", err)
	}

	var users []map[string]any
	if err := json.Unmarshal([]byte(result), &users); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(users) < 2 {
		t.Fatalf("expected at least 2 users (parent + emma), got %d", len(users))
	}

	// Verify required fields are present.
	for _, u := range users {
		if _, ok := u["name"]; !ok {
			t.Error("user record missing 'name'")
		}
		if _, ok := u["role"]; !ok {
			t.Error("user record missing 'role'")
		}
	}
}

func TestAdminSetUserRole_Parent(t *testing.T) {
	db := newAdminTestDB(t)
	ctx := context.Background()

	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatalf("policy.NewEvaluator: %v", err)
	}

	// Terra F12: the real admin role setter writes the shared schema —
	// role is "child", the cohort lives in age_group. Every cohort is
	// verified through the real policy evaluator on input, tool and
	// output decisions.
	cohortCases := []struct {
		cohort    string
		wantInput string // input-policy action for the social_media category
	}{
		{"under_8", "block"},
		{"age_8_12", "request_approval"},
		{"age_13_17", "allow"},
	}
	for _, tc := range cohortCases {
		t.Run(tc.cohort, func(t *testing.T) {
			result, err := admin.HandleSetUserRole(ctx, parentDeps(db), map[string]any{
				"user_name": "emma",
				"role":      "child",
				"age_group": tc.cohort,
			})
			if err != nil {
				t.Fatalf("HandleSetUserRole: %v", err)
			}

			var resp map[string]any
			if err := json.Unmarshal([]byte(result), &resp); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			if resp["role"] != "child" {
				t.Errorf("role = %v, want 'child'", resp["role"])
			}
			if resp["age_group"] != tc.cohort {
				t.Errorf("age_group = %v, want %q", resp["age_group"], tc.cohort)
			}
			if resp["user_name"] != "emma" {
				t.Errorf("user_name = %v, want 'emma'", resp["user_name"])
			}

			// The override is persisted with the shared schema: the role
			// column holds "child" (never a cohort value) and the cohort
			// lives in age_group.
			role, ageGroup, err := db.GetRoleOverride(ctx, "emma")
			if err != nil {
				t.Fatalf("GetRoleOverride: %v", err)
			}
			if role != "child" {
				t.Errorf("stored role override = %q, want 'child'", role)
			}
			if ageGroup != tc.cohort {
				t.Errorf("stored age_group override = %q, want %q", ageGroup, tc.cohort)
			}

			// Every consumer resolves the same effective values.
			effRole, effAge, err := db.GetEffectiveRoleAge(ctx, "emma", "child", "age_8_12")
			if err != nil {
				t.Fatalf("GetEffectiveRoleAge: %v", err)
			}
			if effRole != "child" || effAge != tc.cohort {
				t.Errorf("effective = %q/%q, want child/%q", effRole, effAge, tc.cohort)
			}

			user := policy.UserInput{Role: effRole, AgeGroup: effAge, Name: "emma"}

			// Input policy through the real evaluator.
			decision, err := ev.Evaluate(ctx, policy.Input{
				User:  user,
				Query: policy.QueryInput{Category: "social_media", Text: "can I use instagram"},
			})
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if decision.Action != tc.wantInput {
				t.Errorf("input policy action = %q, want %q", decision.Action, tc.wantInput)
			}

			// Tool policy: file_read must be allowed for the child role in
			// every cohort. (F12 bug: with the cohort value persisted in the
			// role column, neither "parent" nor "child" matched and the tool
			// gate default-denied every tool.)
			toolDecision, err := ev.EvaluateToolCall(ctx, policy.ToolCallInput{
				User:     user,
				ToolName: "file_read",
			})
			if err != nil {
				t.Fatalf("EvaluateToolCall: %v", err)
			}
			if toolDecision.Action != "allow" {
				t.Errorf("tool policy action = %q, want 'allow'", toolDecision.Action)
			}

			// Output policy: the child role must match the PII rule so a
			// response containing a PII keyword is denied, not default-deny
			// by accident.
			outDecision, err := ev.EvaluateOutput(ctx, policy.OutputInput{
				User:          user,
				Gateway:       "telegram",
				DraftResponse: "your ssn is 123-45-6789",
			})
			if err != nil {
				t.Fatalf("EvaluateOutput: %v", err)
			}
			if outDecision.Allow {
				t.Errorf("output policy allowed a PII-bearing response, want deny")
			}

			// Round-trip: list_users must surface the effective override.
			listResult, err := admin.HandleListUsers(ctx, parentDeps(db), map[string]any{})
			if err != nil {
				t.Fatalf("HandleListUsers: %v", err)
			}
			var users []map[string]any
			if err := json.Unmarshal([]byte(listResult), &users); err != nil {
				t.Fatalf("unmarshal list_users result: %v", err)
			}
			var found map[string]any
			for _, u := range users {
				if u["name"] == "emma" {
					found = u
					break
				}
			}
			if found == nil {
				t.Fatalf("emma not in list_users output")
			}
			if found["role"] != "child" {
				t.Errorf("list_users effective role = %v, want 'child'", found["role"])
			}
			if found["age_group"] != tc.cohort {
				t.Errorf("list_users effective age_group = %v, want %q", found["age_group"], tc.cohort)
			}
			if found["has_role_override"] != true {
				t.Errorf("has_role_override = %v, want true", found["has_role_override"])
			}
		})
	}

	assertAuditLogged(t, db, "builtin__set_user_role", "parent", "child", "age_13_17")
}

// TestAdminSetUserRole_PromotionBlocked pins Terra F13: an override may never
// promote a configured non-parent into the parent role. The child user "emma"
// has the parent role in config only for the demotion tests below; the
// promotion case targets emma while she is a configured child.
func TestAdminSetUserRole_PromotionBlocked(t *testing.T) {
	db := newAdminTestDB(t)
	ctx := context.Background()

	// emma is configured as child (role "child", cohort age_8_12).
	if _, err := admin.HandleSetUserRole(ctx, parentDeps(db), map[string]any{
		"user_name": "emma",
		"role":      "parent",
	}); err == nil {
		t.Fatal("set_user_role granted parent role to a configured child — overrides must not promote")
	}
	// Nothing was persisted.
	role, ageGroup, err := db.GetRoleOverride(ctx, "emma")
	if err != nil {
		t.Fatalf("GetRoleOverride: %v", err)
	}
	if role != "" || ageGroup != "" {
		t.Errorf("override row persisted after blocked promotion: %q/%q", role, ageGroup)
	}

	// A raw legacy/malicious row (role=parent for a base child) is capped at
	// the read path: effective role is "child", never "parent".
	if err := db.SetRoleOverride(ctx, "emma", "parent", "", "test"); err != nil {
		t.Fatalf("SetRoleOverride: %v", err)
	}
	t.Cleanup(func() { _ = db.DeleteRoleOverride(ctx, "emma") })
	effRole, _, err := db.GetEffectiveRoleAge(ctx, "emma", "child", "age_8_12")
	if err != nil {
		t.Fatalf("GetEffectiveRoleAge: %v", err)
	}
	if effRole != "child" {
		t.Errorf("effective role for base child with parent override = %q, want 'child'", effRole)
	}

	// Invalid inputs are rejected.
	if _, err := admin.HandleSetUserRole(ctx, parentDeps(db), map[string]any{
		"user_name": "emma",
		"role":      "under_8",
	}); err == nil {
		t.Error("cohort value accepted as role — shared schema is role parent|child")
	}
	if _, err := admin.HandleSetUserRole(ctx, parentDeps(db), map[string]any{
		"user_name": "emma",
		"role":      "parent",
		"age_group": "under_8",
	}); err == nil {
		t.Error("age_group accepted with role parent — parents carry no cohort")
	}
}

// TestAdminSetUserRole_DemotionAndRecovery pins the administrative demotion
// and recovery semantics (Terra F13): a configured parent can be demoted to
// child, the demotion is visible through effective resolution, and re-setting
// role=parent on the configured parent clears the override (recovery) without
// granting anything to a base non-parent.
func TestAdminSetUserRole_DemotionAndRecovery(t *testing.T) {
	db := newAdminTestDB(t)
	ctx := context.Background()

	// Demote the configured parent.
	if _, err := admin.HandleSetUserRole(ctx, parentDeps(db), map[string]any{
		"user_name": "parent",
		"role":      "child",
		"age_group": "under_8",
	}); err != nil {
		t.Fatalf("HandleSetUserRole (demote): %v", err)
	}
	effRole, effAge, err := db.GetEffectiveRoleAge(ctx, "parent", "parent", "")
	if err != nil {
		t.Fatalf("GetEffectiveRoleAge: %v", err)
	}
	if effRole != "child" || effAge != "under_8" {
		t.Errorf("demoted parent effective = %q/%q, want child/under_8", effRole, effAge)
	}

	// Recovery: role=parent on a configured parent clears the override.
	if _, err := admin.HandleSetUserRole(ctx, parentDeps(db), map[string]any{
		"user_name": "parent",
		"role":      "parent",
	}); err != nil {
		t.Fatalf("HandleSetUserRole (recovery): %v", err)
	}
	role, ageGroup, err := db.GetRoleOverride(ctx, "parent")
	if err != nil {
		t.Fatalf("GetRoleOverride: %v", err)
	}
	if role != "" || ageGroup != "" {
		t.Errorf("override row still present after recovery: %q/%q", role, ageGroup)
	}
	effRole, _, err = db.GetEffectiveRoleAge(ctx, "parent", "parent", "")
	if err != nil {
		t.Fatalf("GetEffectiveRoleAge: %v", err)
	}
	if effRole != "parent" {
		t.Errorf("effective role after recovery = %q, want 'parent'", effRole)
	}
}

func TestAdminListUnknownAccounts_Parent(t *testing.T) {
	db := newAdminTestDB(t)
	ctx := context.Background()

	insertUnknownAccount(t, db, "telegram", "tg-unknown-123", "Unknown User")

	result, err := admin.HandleListUnknownAccounts(ctx, parentDeps(db), map[string]any{})
	if err != nil {
		t.Fatalf("HandleListUnknownAccounts: %v", err)
	}

	var accounts []map[string]any
	if err := json.Unmarshal([]byte(result), &accounts); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(accounts) < 1 {
		t.Fatalf("expected at least 1 unknown account, got %d", len(accounts))
	}

	found := false
	for _, a := range accounts {
		if a["external_id"] == "tg-unknown-123" {
			found = true
			if a["gateway"] != "telegram" {
				t.Errorf("gateway = %v, want 'telegram'", a["gateway"])
			}
		}
	}
	if !found {
		t.Error("inserted unknown account not found in list result")
	}
}

func TestAdminLinkAccount_Parent(t *testing.T) {
	db := newAdminTestDB(t)
	ctx := context.Background()

	accountID := insertUnknownAccount(t, db, "discord", "dc-unknown-456", "Stranger")

	result, err := admin.HandleLinkAccount(ctx, parentDeps(db), map[string]any{
		"account_id": float64(accountID), // JSON numbers decode as float64
		"user_name":  "emma",
	})
	if err != nil {
		t.Fatalf("HandleLinkAccount: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if resp["user_name"] != "emma" {
		t.Errorf("user_name = %v, want 'emma'", resp["user_name"])
	}
	if resp["external_id"] != "dc-unknown-456" {
		t.Errorf("external_id = %v, want 'dc-unknown-456'", resp["external_id"])
	}

	// Verify the account is now linked (no longer in unknown_accounts).
	accounts, err := db.ListUnknownAccounts(ctx)
	if err != nil {
		t.Fatalf("ListUnknownAccounts after link: %v", err)
	}
	for _, a := range accounts {
		if a.ExternalID == "dc-unknown-456" {
			t.Error("linked account still appears in unknown_accounts after link")
		}
	}

	// Verify it is now in gateway_accounts.
	linkedUser, err := db.ResolveGatewayAccount(ctx, "discord", "dc-unknown-456")
	if err != nil {
		t.Fatalf("ResolveGatewayAccount: %v", err)
	}
	if linkedUser != "emma" {
		t.Errorf("linked user = %q, want 'emma'", linkedUser)
	}

	assertAuditLogged(t, db, "builtin__link_account", "parent", "emma")
}

// ── Role enforcement ─────────────────────────────────────────────────────────
//
// Admin tools are gated at two layers:
//  1. agentcore.Tool.AllowedForRole — definition-level gate; returns false for
//     any non-parent role. Covered here.
//  2. OPA tool_policy.rego — runtime gate at request evaluation time. Covered
//     by the Rego unit tests in tool_policy_test.rego.
//
// Handlers themselves do NOT check roles (OPA's responsibility), so testing at
// the tool-definition level is the correct enforcement point in this layer.

// TestAdminTools_RoleEnforcement verifies that every admin tool definition
// reports AllowedForRole == false for every non-parent role and == true for
// "parent". Replaces seven near-duplicate per-tool denial tests.
func TestAdminTools_RoleEnforcement(t *testing.T) {
	denyRoles := []string{"child", "under_8", "age_8_12", "age_13_17"}
	defs := admin.AllDefinitions()
	if len(defs) == 0 {
		t.Fatal("admin.AllDefinitions() returned no tools")
	}

	for _, tool := range defs {
		tool := tool
		t.Run(tool.Name, func(t *testing.T) {
			if !tool.AllowedForRole("parent") {
				t.Errorf("%s should be allowed for role 'parent'", tool.Name)
			}
			for _, role := range denyRoles {
				if tool.AllowedForRole(role) {
					t.Errorf("%s should NOT be allowed for role %q", tool.Name, role)
				}
			}
		})
	}
}
