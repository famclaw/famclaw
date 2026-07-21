// Package agent — sandbox confinement tests for the builtin file_* tools
// (file_read, file_write, file_stat, file_list). These exercise the
// confinePath / handleFileWrite escape-prevention and the 0600 default
// mode added in response to PR #188 independent review findings.
package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// newFileAgent builds a minimal Agent wired only enough to drive
// handleFileWrite / confinePath directly. It uses a real on-disk
// sandbox root inside t.TempDir() and a parent-role config so the
// policy evaluator is not relevant here.
func newFileAgent(t *testing.T, sandboxRoot string) *Agent {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatalf("policy.NewEvaluator: %v", err)
	}

	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent"},
		},
		Tools: config.ToolsConfig{
			SandboxRoot: sandboxRoot,
		},
	}
	return &Agent{
		user:                 &cfg.Users[0],
		cfg:                  cfg,
		evaluator:          ev,
		classifier:           classifier.New(),
		db:                   db,
		effectiveSandboxRoot: sandboxRoot,
	}
}

func TestConfinePath_Table(t *testing.T) {
	sandbox := t.TempDir()
	// Pre-create the file and subdirectory consumed by the happy-path
	// sub-tests. EvalSymlinks refuses non-existent paths so callers must
	// provide existing on-disk layout; confinePath itself does not create.
	mustMkdir(t, filepath.Join(sandbox, "sub", "dir"))
	mustWrite(t, filepath.Join(sandbox, "note.txt"), "x")
	a := newFileAgent(t, sandbox)

	tests := []struct {
		name       string
		path       string
		wantErr    bool
		wantInSand bool
	}{
		{name: "abs path inside sandbox ok", path: filepath.Join(sandbox, "note.txt"), wantErr: false, wantInSand: true},
		{name: "rel path inside sandbox ok", path: "note.txt", wantErr: false, wantInSand: true},
		{name: "rel path with subdir ok", path: filepath.Join("sub", "dir"), wantErr: false, wantInSand: true},
		{name: "abs path outside sandbox blocked", path: "/etc/passwd", wantErr: true, wantInSand: false},
		{name: "rel ../escape blocked", path: "../escape.txt", wantErr: true, wantInSand: false},
		{name: "rel deep escape blocked", path: filepath.Join("sub", "..", "..", "..", "..", "etc", "passwd"), wantErr: true, wantInSand: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := a.confinePath(tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for path %q, got %q", tc.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for path %q: %v", tc.path, err)
			}
			if tc.wantInSand && !strings.HasPrefix(got, sandbox) && !strings.HasPrefix(got, sandbox+string(os.PathSeparator)) {
				t.Fatalf("path %q not within sandbox %q", got, sandbox)
			}
		})
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0700); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
}

func mustWrite(t *testing.T, p string, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// TestHandleFileWrite_ReconfinesAfterJoin is the regression test for the
// review finding that filepath.Join(confinedDir, base) inside the sandbox
// is not re-checked. A pathological ".."-only base collapses the joined
// path back to the directory above the sandbox and must be rejected even
// though confinePath already approved the directory.
func TestHandleFileWrite_ReconfinesAfterJoin(t *testing.T) {
	sandbox := t.TempDir()
	a := newFileAgent(t, sandbox)

	escapes := []string{
		"..",
		"./..",
		"sub/../..",
		"a/b/../../../..",
	}
	for _, p := range escapes {
		t.Run(p, func(t *testing.T) {
			_, err := a.handleFileWrite(context.Background(), map[string]any{
				"path":    p,
				"content": "x",
			})
			if err == nil {
				t.Fatalf("expected escape error for path %q, got nil", p)
			}
			if !strings.Contains(err.Error(), "sandbox") {
				t.Fatalf("expected sandbox-escape error, got %v", err)
			}
		})
	}
}

// TestHandleFileWrite_WritesWith0600 verifies the file is created with
// 0600 permissions (the second review finding — restrictive default
// instead of 0644). Uses t.TempDir() for the sandbox so cleanup is
// implicit.
func TestHandleFileWrite_WritesWith0600(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits are not meaningful when running as root")
	}
	sandbox := t.TempDir()
	a := newFileAgent(t, sandbox)

	if _, err := a.handleFileWrite(context.Background(), map[string]any{
		"path":    "secret.txt",
		"content": "classified",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	full := filepath.Join(sandbox, "secret.txt")
	info, err := os.Stat(full)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("expected mode 0600, got %#o", perm)
	}
}

// TestHandleFileWrite_SymlinkEscapeBlocked plants a symlink inside the
// sandbox that points at a path outside it, then attempts to write
// through that symlink. handleFileWrite must reject the resolved path
// even when the directory-level confinement check has passed.
func TestHandleFileWrite_SymlinkEscapeBlocked(t *testing.T) {
	sandbox := t.TempDir()
	// Lay down a "shadow" file outside the sandbox that the symlink
	// points to. If the escape succeeds, this file is overwritten — a
	// real-world break-out.
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.txt")
	mustWrite(t, outsideFile, "untouched")

	linkName := "alias.txt"
	if err := os.Symlink(outsideFile, filepath.Join(sandbox, linkName)); err != nil {
		t.Skipf("symlinks unsupported on this filesystem: %v", err)
	}

	a := newFileAgent(t, sandbox)
	_, err := a.handleFileWrite(context.Background(), map[string]any{
		"path":    linkName,
		"content": "OVERWRITE",
	})
	if err == nil {
		t.Fatalf("expected symlink-escape error, got nil")
	}
	// Confirm the outside file was NOT modified.
	body, readErr := os.ReadFile(outsideFile)
	if readErr != nil {
		t.Fatalf("read outside: %v", readErr)
	}
	if string(body) != "untouched" {
		t.Fatalf("outside file was modified through symlink: %q", body)
	}
}

// TestIsWithinDir covers the helper that powers the symlink-escape check.
func TestIsWithinDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	mustMkdir(t, root)
	child := filepath.Join(root, "sub")
	mustMkdir(t, child)
	grandchild := filepath.Join(child, "deep.txt")
	mustWrite(t, grandchild, "x")

	tests := []struct {
		path string
		dir  string
		want bool
	}{
		{path: grandchild, dir: root, want: true},
		{path: root, dir: root, want: true},
		{path: filepath.Dir(root), dir: root, want: false},
		{path: "/etc/passwd", dir: root, want: false},
		// Reject the prefix-spoof case "/rootABC" vs "/root".
		{path: root + "ABC", dir: root, want: false},
	}
	for _, tc := range tests {
		got := isWithinDir(tc.path, tc.dir)
		if got != tc.want {
			t.Errorf("isWithinDir(%q, %q) = %v, want %v", tc.path, tc.dir, got, tc.want)
		}
	}
}

// TestComputeEffectiveSandboxRoot_PerUserPerGroup tests the computeEffectiveSandboxRoot function
// with the exact requirements from the task.
func TestComputeEffectiveSandboxRoot_PerUserPerGroup(t *testing.T) {
	base := t.TempDir()

	newCfg := func(scope string) *config.Config {
		c := &config.Config{}
		c.Tools.SandboxRoot = base
		c.Tools.SandboxScope = scope
		return c
	}

	// user scope: two different users get two different roots, both under base
	userCfg := newCfg("user")
	rootA, err := computeEffectiveSandboxRoot(userCfg, gateway.MsgContext{ExternalID: "userA"})
	if err != nil {
		t.Fatalf("userA: %v", err)
	}
	rootB, err := computeEffectiveSandboxRoot(userCfg, gateway.MsgContext{ExternalID: "userB"})
	if err != nil {
		t.Fatalf("userB: %v", err)
	}
	if rootA == rootB {
		t.Fatalf("distinct users must NOT share a sandbox root: both got %q", rootA)
	}
	if !strings.HasPrefix(rootA, base) || !strings.HasPrefix(rootB, base) {
		t.Fatalf("user roots must live under base %q: A=%q B=%q", base, rootA, rootB)
	}

	// same user -> stable, identical root
	rootA2, err := computeEffectiveSandboxRoot(userCfg, gateway.MsgContext{ExternalID: "userA"})
	if err != nil {
		t.Fatalf("userA repeat: %v", err)
	}
	if rootA != rootA2 {
		t.Fatalf("same user must get the same root: %q vs %q", rootA, rootA2)
	}

	// group scope: members of the same group SHARE one root; different groups differ
	groupCfg := newCfg("group")
	g1a, err := computeEffectiveSandboxRoot(groupCfg, gateway.MsgContext{GroupID: "fam1"})
	if err != nil {
		t.Fatalf("group fam1 memberA: %v", err)
	}
	g1b, err := computeEffectiveSandboxRoot(groupCfg, gateway.MsgContext{GroupID: "fam1"})
	if err != nil {
		t.Fatalf("group fam1 memberB: %v", err)
	}
	if g1a != g1b {
		t.Fatalf("members of the same group must SHARE a root: %q vs %q", g1a, g1b)
	}
	g2, err := computeEffectiveSandboxRoot(groupCfg, gateway.MsgContext{GroupID: "fam2"})
	if err != nil {
		t.Fatalf("group fam2: %v", err)
	}
	if g1a == g2 {
		t.Fatalf("different groups must NOT share a root: both got %q", g1a)
	}

	// a traversal-laden identity must not escape base (sanitized or rejected)
	esc, err := computeEffectiveSandboxRoot(userCfg, gateway.MsgContext{ExternalID: "../../etc"})
	if err == nil && !strings.HasPrefix(esc, base) {
		t.Fatalf("traversal identity escaped the base dir: %q", esc)
	}
}
