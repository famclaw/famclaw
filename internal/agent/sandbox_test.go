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

// TestSanitizeDirName tests the sanitizeDirName function with the new allowlist approach.
func TestSanitizeDirName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid identity", input: "alice", wantErr: false},
		{name: "valid identity with underscore", input: "user_1", wantErr: false},
		{name: "valid identity with dot", input: "a.b-c", wantErr: false},
		{name: "valid identity with dash", input: "user-name", wantErr: false},
		{name: "empty identity", input: "", wantErr: true},
		{name: "identity with slash", input: "user/name", wantErr: true},
		{name: "identity with backslash", input: "user\\name", wantErr: true},
		{name: "identity with control char", input: "user\x00name", wantErr: true},
		{name: "identity with null char", input: "user\x00name", wantErr: true},
		{name: "identity with special char", input: "user@name", wantErr: true},
		{name: "identity with space", input: "user name", wantErr: true},
		{name: "identity with colon", input: "user:name", wantErr: true},
		{name: "identity with pipe", input: "user|name", wantErr: true},
		{name: "identity with question mark", input: "user?name", wantErr: true},
		{name: "identity with asterisk", input: "user*name", wantErr: true},
		{name: "identity with greater than", input: "user>name", wantErr: true},
		{name: "identity with less than", input: "user<name", wantErr: true},
		{name: "identity with tilde", input: "user~name", wantErr: true},
		{name: "identity with percent", input: "user%name", wantErr: true},
		{name: "identity with ampersand", input: "user&name", wantErr: true},
		{name: "identity with exclamation", input: "user!name", wantErr: true},
		{name: "identity with dollar", input: "user$name", wantErr: true},
		{name: "identity with hash", input: "user#name", wantErr: true},
		{name: "identity with caret", input: "user^name", wantErr: true},
		{name: "identity with paren", input: "user(name", wantErr: true},
		{name: "identity with bracket", input: "user[name", wantErr: true},
		{name: "identity with brace", input: "user{name", wantErr: true},
		{name: "identity with square bracket", input: "user[name", wantErr: true},
		{name: "identity with comma", input: "user,name", wantErr: true},
		{name: "identity with semicolon", input: "user;name", wantErr: true},
		{name: "identity with equals", input: "user=name", wantErr: true},
		{name: "identity with plus", input: "user+name", wantErr: true},
		{name: "identity with slash in middle", input: "user/nam/e", wantErr: true},
		{name: "identity with backslash in middle", input: "user\\nam\\e", wantErr: true},
		{name: "reserved identity dot", input: ".", wantErr: true},
		{name: "reserved identity dotdot", input: "..", wantErr: true},
		{name: "long identity", input: strings.Repeat("a", 256), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sanitizeDirName(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got nil", tc.input)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for input %q: %v", tc.input, err)
				}
			}
		})
	}
}

// TestComputeEffectiveSandboxRoot_ContainmentAssert tests the containment assertion
// in computeEffectiveSandboxRoot to ensure computed roots stay within base.
func TestComputeEffectiveSandboxRoot_ContainmentAssert(t *testing.T) {
	base := t.TempDir()

	newCfg := func(scope string) *config.Config {
		c := &config.Config{}
		c.Tools.SandboxRoot = base
		c.Tools.SandboxScope = scope
		return c
	}

	// Test with a path that would escape if not properly checked
	// Using a path that contains ".." or other traversal characters
	// that would normally be sanitized but might still escape if not properly checked
	userCfg := newCfg("user")
	
	// This should be rejected because it contains disallowed characters
	_, err := computeEffectiveSandboxRoot(userCfg, gateway.MsgContext{ExternalID: "user../etc"})
	if err == nil {
		t.Fatalf("Expected error for identity with traversal chars, got nil")
	}
	
	// This should work correctly with valid identities
	root, err := computeEffectiveSandboxRoot(userCfg, gateway.MsgContext{ExternalID: "alice"})
	if err != nil {
		t.Fatalf("Unexpected error for valid identity: %v", err)
	}
	
	// Verify it's within base
	if !strings.HasPrefix(root, base) {
		t.Fatalf("Computed root %q is not within base %q", root, base)
	}
	
	// Test with a specially crafted path that might try to escape
	// This tests our containment assertion specifically
	escapedRoot, err := computeEffectiveSandboxRoot(userCfg, gateway.MsgContext{ExternalID: "user.."})
	if err != nil {
		// This should fail because ".." is not allowed by the new allowlist
		t.Logf("Expected error for invalid identity: %v", err)
	} else {
		// If it passes, make sure it's still contained
		if !strings.HasPrefix(escapedRoot, base) {
			t.Fatalf("Escaped root %q is not within base %q", escapedRoot, base)
		}
	}
}
