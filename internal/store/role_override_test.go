package store

import (
	"context"
	"path/filepath"
	"testing"
)

// Terra F12/F13 regression for the shared role/age schema: role is always
// "parent" or "child" (never a cohort value), the cohort lives in age_group,
// and an override may demote but never promote. These pin the behaviour of
// every consumer of the effective role (gateway input/tool/output policy and
// the web management gate resolve through GetEffectiveRoleAge).

func newRoleOverrideTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "role.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestEffectiveRoleAge_SchemaAndCap pins the shared-schema read rules for
// every override/config combination the tool can (or legacy code could)
// write.
func TestEffectiveRoleAge_SchemaAndCap(t *testing.T) {
	db := newRoleOverrideTestDB(t)
	ctx := context.Background()

	cases := []struct {
		name           string
		user           string
		overrideRole   string // seeded raw row; empty = no row
		overrideAge    string
		cfgRole        string
		cfgAge         string
		wantRole       string
		wantAge        string
	}{
		{"no override keeps config", "emma", "", "", "child", "age_8_12", "child", "age_8_12"},
		{"no override keeps parent config", "parent", "", "", "parent", "", "parent", ""},
		{"cohort override persists role child", "emma", "child", "under_8", "child", "age_8_12", "child", "under_8"},
		{"age-only override falls back role to config", "emma", "", "age_13_17", "child", "age_8_12", "child", "age_13_17"},
		{"role-only override falls back age to config", "emma", "child", "", "child", "age_8_12", "child", "age_8_12"},
		{"legacy cohort-valued role normalized to child", "emma", "under_8", "under_8", "child", "age_8_12", "child", "under_8"},
		{"legacy age_13_17 role normalized to child", "emma", "age_13_17", "age_13_17", "child", "age_8_12", "child", "age_13_17"},
		{"parent override on base child capped to child (no promotion)", "emma", "parent", "", "child", "age_8_12", "child", "age_8_12"},
		{"parent override on base child with cohort capped, cohort kept", "emma", "parent", "under_8", "child", "age_8_12", "child", "under_8"},
		{"demotion of base parent honored", "parent", "child", "under_8", "parent", "", "child", "under_8"},
		{"parent override on base parent kept", "parent", "parent", "", "parent", "", "parent", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = db.DeleteRoleOverride(ctx, tc.user)
			if tc.overrideRole != "" || tc.overrideAge != "" {
				if err := db.SetRoleOverride(ctx, tc.user, tc.overrideRole, tc.overrideAge, "test"); err != nil {
					t.Fatalf("SetRoleOverride: %v", err)
				}
			}
			role, age, err := db.GetEffectiveRoleAge(ctx, tc.user, tc.cfgRole, tc.cfgAge)
			if err != nil {
				t.Fatalf("GetEffectiveRoleAge: %v", err)
			}
			if role != tc.wantRole {
				t.Errorf("effective role = %q, want %q", role, tc.wantRole)
			}
			if age != tc.wantAge {
				t.Errorf("effective age = %q, want %q", age, tc.wantAge)
			}
			_ = db.DeleteRoleOverride(ctx, tc.user)
		})
	}
}

// TestEffectiveRoleAge_MigrationNormalizesLegacyRows verifies the startup
// migration rewrites pre-fix set_user_role rows (cohort value in the role
// column) into the shared schema: role "child" plus the cohort in age_group.
// The rows are written directly (as the pre-fix tool wrote them), then the
// database is reopened so the migration runs — exactly the upgrade path for
// an existing deployment.
func TestEffectiveRoleAge_MigrationNormalizesLegacyRows(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.db")

	// Phase 1: write legacy rows the way the pre-fix tool did.
	db1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (phase 1): %v", err)
	}
	ctx := context.Background()
	legacy := []struct{ user, role, age string }{
		{"emma", "under_8", "under_8"},
		{"olivia", "age_8_12", "age_8_12"},
		{"jack", "age_13_17", "age_13_17"},
	}
	for _, l := range legacy {
		if err := db1.SetRoleOverride(ctx, l.user, l.role, l.age, "test"); err != nil {
			t.Fatalf("SetRoleOverride %s: %v", l.user, err)
		}
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("Close (phase 1): %v", err)
	}

	// Phase 2: reopen — migration normalizes the rows.
	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (phase 2): %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	for _, l := range legacy {
		role, age, err := db2.GetRoleOverride(ctx, l.user)
		if err != nil {
			t.Fatalf("GetRoleOverride %s: %v", l.user, err)
		}
		if role != "child" {
			t.Errorf("%s: migrated role = %q, want 'child'", l.user, role)
		}
		if age != l.age {
			t.Errorf("%s: migrated age_group = %q, want %q", l.user, age, l.age)
		}
	}

	// Promoting rows (base child with role=parent) survive the migration
	// verbatim — they are capped at the read path (no config context in the
	// DB), pinned by TestEffectiveRoleAge_SchemaAndCap.
	role, _, err := db2.GetEffectiveRoleAge(ctx, "emma", "child", "age_8_12")
	if err != nil {
		t.Fatalf("GetEffectiveRoleAge: %v", err)
	}
	if role != "child" {
		t.Errorf("post-migration effective role for base child = %q, want 'child'", role)
	}
}
