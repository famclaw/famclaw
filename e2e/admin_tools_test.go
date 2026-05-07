//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/agent/tools/admin"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

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

	result, err := admin.HandleSetUserRole(ctx, parentDeps(db), map[string]any{
		"user_name": "emma",
		"role":      "under_8",
	})
	if err != nil {
		t.Fatalf("HandleSetUserRole: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if resp["role"] != "under_8" {
		t.Errorf("role = %v, want 'under_8'", resp["role"])
	}
	if resp["user_name"] != "emma" {
		t.Errorf("user_name = %v, want 'emma'", resp["user_name"])
	}

	// Verify override is stored in DB.
	overrideRole, _, err := db.GetRoleOverride(ctx, "emma")
	if err != nil {
		t.Fatalf("GetRoleOverride: %v", err)
	}
	if overrideRole != "under_8" {
		t.Errorf("stored role override = %q, want 'under_8'", overrideRole)
	}

	// Round-trip: list_users must surface the override (not the config row).
	listResult, err := admin.HandleListUsers(ctx, parentDeps(db), map[string]any{})
	if err != nil {
		t.Fatalf("HandleListUsers after set_user_role: %v", err)
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
	if found["role"] != "under_8" {
		t.Errorf("list_users effective role = %v, want 'under_8'", found["role"])
	}
	if found["age_group"] != "under_8" {
		t.Errorf("list_users effective age_group = %v, want 'under_8'", found["age_group"])
	}
	if found["has_role_override"] != true {
		t.Errorf("has_role_override = %v, want true", found["has_role_override"])
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
	linkedUser, err := db.ResolveGatewayAccount("discord", "dc-unknown-456")
	if err != nil {
		t.Fatalf("ResolveGatewayAccount: %v", err)
	}
	if linkedUser != "emma" {
		t.Errorf("linked user = %q, want 'emma'", linkedUser)
	}
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
