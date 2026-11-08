package web

import (
	"context"
	"path/filepath"
	"testing"
)

// Terra F13 regression (web surfaces): role overrides may DEMOTE a configured
// parent to child — and the demotion must be honored on every management
// surface, live, on the same session — but may never PROMOTE a configured
// non-parent into the parent role.

// TestDemotedParentLosesAdminAccessLive: a demotion persisted while the parent
// is logged in takes effect on the next request to every admin route, and
// clears the moment the override is removed (administrative recovery).
func TestDemotedParentLosesAdminAccessLive(t *testing.T) {
	ts, _, sessions, db := newPrincipalTestServer(t, filepath.Join(t.TempDir(), "test.db"), principalTestUsers())
	ctx := context.Background()

	sess, err := sessions.Create(ctx, "parent", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create (parent): %v", err)
	}

	// Control: the configured parent manages before any override.
	if code, _ := getWithSession(t, ts, "/api/users", sess); code != 200 {
		t.Fatalf("pre-demotion parent GET /api/users = %d, want 200", code)
	}

	adminPaths := []string{
		"/api/users",
		"/api/approvals",
		"/api/settings",
		"/api/conversations?user=emma",
		"/api/reminders/undelivered",
	}

	// Demotion: persisted override child/under_8 for the configured parent.
	if err := db.SetRoleOverride(ctx, "parent", "child", "under_8", "test"); err != nil {
		t.Fatalf("SetRoleOverride: %v", err)
	}
	defer db.DeleteRoleOverride(ctx, "parent")

	for _, p := range adminPaths {
		if code, body := getWithSession(t, ts, p, sess); code != 403 {
			t.Errorf("demoted parent GET %s = %d, want 403 (body=%s)", p, code, body)
		}
	}

	// The session itself is still authenticated (authn is not revoked) — the
	// holder is simply no longer a manager.
	if code, _ := getWithSession(t, ts, "/session", sess); code != 200 {
		t.Fatalf("demoted parent /session = %d, want 200", code)
	}

	// Recovery: clearing the override restores management on the same session.
	if err := db.DeleteRoleOverride(ctx, "parent"); err != nil {
		t.Fatalf("DeleteRoleOverride: %v", err)
	}
	if code, _ := getWithSession(t, ts, "/api/users", sess); code != 200 {
		t.Fatalf("post-recovery parent GET /api/users = %d, want 200", code)
	}
}

// TestPromotionOverrideGrantsNoAdminAccess: a raw override row that names
// role=parent for a configured CHILD (legacy, hand-edited DB, or pre-fix
// write) must not grant management access anywhere on the web surface.
func TestPromotionOverrideGrantsNoAdminAccess(t *testing.T) {
	ts, _, sessions, db := newPrincipalTestServer(t, filepath.Join(t.TempDir(), "test.db"), principalTestUsers())
	ctx := context.Background()

	emmaSess, err := sessions.Create(ctx, "emma", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("sessions.Create (emma): %v", err)
	}

	// Malicious/legacy promotion row.
	if err := db.SetRoleOverride(ctx, "emma", "parent", "", "test"); err != nil {
		t.Fatalf("SetRoleOverride: %v", err)
	}
	defer db.DeleteRoleOverride(ctx, "emma")

	for _, p := range []string{"/api/users", "/api/approvals", "/api/settings", "/api/conversations?user=parent"} {
		if code, body := getWithSession(t, ts, p, emmaSess); code != 403 {
			t.Errorf("promoted-override emma GET %s = %d, want 403 (body=%s)", p, code, body)
		}
	}

	// Her identity is unchanged — still emma, not some parent slot.
	if code, body := getWithSession(t, ts, "/session", emmaSess); code != 200 {
		t.Fatalf("emma /session = %d, want 200 (body=%s)", code, body)
	}
}

// TestResolveUserRoleSharedSchema pins the effective-role resolution the web
// chat pipeline uses, against the shared schema (F12) and the no-promotion
// cap (F13).
func TestResolveUserRoleSharedSchema(t *testing.T) {
	_, s, _, db := newPrincipalTestServer(t, filepath.Join(t.TempDir(), "test.db"), principalTestUsers())
	ctx := context.Background()

	cases := []struct {
		name         string
		user         string
		overrideRole string // "" = no override row
		overrideAge  string
		wantRole     string
		wantAge      string
	}{
		{"no override keeps config", "emma", "", "", "child", "age_8_12"},
		{"parent override on base child is capped", "emma", "parent", "", "child", "age_8_12"},
		{"legacy cohort role is normalized", "emma", "under_8", "under_8", "child", "under_8"},
		{"child override changes cohort", "emma", "child", "age_13_17", "child", "age_13_17"},
		{"age-only override keeps role", "emma", "", "under_8", "child", "under_8"},
		{"parent demotion honored", "parent", "child", "under_8", "child", "under_8"},
		{"parent without override", "parent", "", "", "parent", ""},
		{"unknown user", "ghost", "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = db.DeleteRoleOverride(ctx, tc.user)
			if tc.overrideRole != "" || tc.overrideAge != "" {
				if err := db.SetRoleOverride(ctx, tc.user, tc.overrideRole, tc.overrideAge, "test"); err != nil {
					t.Fatalf("SetRoleOverride: %v", err)
				}
			}
			got := s.resolveUserRole(ctx, tc.user)
			if tc.user == "ghost" {
				if got != nil {
					t.Fatalf("resolveUserRole(ghost) = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("resolveUserRole(%s) = nil", tc.user)
			}
			if got.Role != tc.wantRole {
				t.Errorf("Role = %q, want %q", got.Role, tc.wantRole)
			}
			if got.AgeGroup != tc.wantAge {
				t.Errorf("AgeGroup = %q, want %q", got.AgeGroup, tc.wantAge)
			}
			_ = db.DeleteRoleOverride(ctx, tc.user)
		})
	}
}

