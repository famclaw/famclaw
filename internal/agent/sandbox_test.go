// Package agent — sandbox confinement tests for the builtin file_* tools
// (file_read, file_write, file_stat, file_list). These exercise the
// confinePath / handleFileWrite escape-prevention and the 0600 default
// mode added in response to PR #188 independent review findings.
//
// Per-conversation sandbox partitioning (replacing the shelved per-user
// scope from issue #221) is tested via computeEffectiveSandboxRoot and
// conversationSandboxRoot.
package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/familystate"
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
		evaluator:            ev,
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

// TestSanitizeDirName tests the sanitizeDirName function with the strict
// allowlist approach.
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

// convCfg returns a config with the given base and "conversation" scope.
func convCfg(base string) *config.Config {
	return &config.Config{
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent"},
		},
		Tools: config.ToolsConfig{
			SandboxRoot:  base,
			SandboxScope: "conversation",
		},
	}
}

// TestConversationSandbox_DifferentDMsDifferentRoots verifies that two
// different DMs (different external IDs) on the same gateway resolve to
// different sandbox roots.
func TestConversationSandbox_DifferentDMsDifferentRoots(t *testing.T) {
	base := t.TempDir()
	cfg := convCfg(base)

	rootAlice, err := computeEffectiveSandboxRoot(cfg, gateway.MsgContext{
		Gateway: "telegram", ExternalID: "alice", IsGroup: false,
	})
	if err != nil {
		t.Fatalf("alice root: %v", err)
	}
	rootBob, err := computeEffectiveSandboxRoot(cfg, gateway.MsgContext{
		Gateway: "telegram", ExternalID: "bob", IsGroup: false,
	})
	if err != nil {
		t.Fatalf("bob root: %v", err)
	}
	if rootAlice == rootBob {
		t.Fatalf("expected different roots for different DMs; both %q", rootAlice)
	}
}

// TestConversationSandbox_DMAndChannelDifferentRoots verifies that a DM
// and a channel on the same gateway resolve to different sandbox roots.
func TestConversationSandbox_DMAndChannelDifferentRoots(t *testing.T) {
	base := t.TempDir()
	cfg := convCfg(base)

	rootDM, err := computeEffectiveSandboxRoot(cfg, gateway.MsgContext{
		Gateway: "telegram", ExternalID: "alice", IsGroup: false,
	})
	if err != nil {
		t.Fatalf("DM root: %v", err)
	}
	rootChannel, err := computeEffectiveSandboxRoot(cfg, gateway.MsgContext{
		Gateway: "telegram", GroupID: "family-chat", IsGroup: true,
	})
	if err != nil {
		t.Fatalf("channel root: %v", err)
	}
	if rootDM == rootChannel {
		t.Fatalf("expected different roots for DM and channel; both %q", rootDM)
	}
}

// TestConversationSandbox_StableKeying verifies that the same conversation
// across two calls resolves to the SAME root.
func TestConversationSandbox_StableKeying(t *testing.T) {
	base := t.TempDir()
	cfg := convCfg(base)

	ctx := gateway.MsgContext{
		Gateway: "telegram", ExternalID: "alice", IsGroup: false,
	}
	root1, err := computeEffectiveSandboxRoot(cfg, ctx)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	root2, err := computeEffectiveSandboxRoot(cfg, ctx)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if root1 != root2 {
		t.Fatalf("expected same root across calls; got %q and %q", root1, root2)
	}
}

// TestConversationSandbox_DiffGatewayDifferentRoots verifies that the same
// user on different gateways gets different sandbox roots (the key
// improvement over the shelved per-user scope).
func TestConversationSandbox_DiffGatewayDifferentRoots(t *testing.T) {
	base := t.TempDir()
	cfg := convCfg(base)

	rootTG, err := computeEffectiveSandboxRoot(cfg, gateway.MsgContext{
		Gateway: "telegram", ExternalID: "alice", IsGroup: false,
	})
	if err != nil {
		t.Fatalf("telegram root: %v", err)
	}
	rootDC, err := computeEffectiveSandboxRoot(cfg, gateway.MsgContext{
		Gateway: "discord", ExternalID: "alice", IsGroup: false,
	})
	if err != nil {
		t.Fatalf("discord root: %v", err)
	}
	if rootTG == rootDC {
		t.Fatalf("expected different roots for same user on different gateways; both %q", rootTG)
	}
}

// TestConversationSandbox_GroupStableKeying verifies that a group channel
// resolves the same root regardless of which member sends.
func TestConversationSandbox_GroupStableKeying(t *testing.T) {
	base := t.TempDir()
	cfg := convCfg(base)

	// Alice sending in the family group.
	rootAlice, err := computeEffectiveSandboxRoot(cfg, gateway.MsgContext{
		Gateway: "telegram", ExternalID: "alice", GroupID: "fam123", IsGroup: true,
	})
	if err != nil {
		t.Fatalf("alice in group: %v", err)
	}
	// Bob sending in the same family group.
	rootBob, err := computeEffectiveSandboxRoot(cfg, gateway.MsgContext{
		Gateway: "telegram", ExternalID: "bob", GroupID: "fam123", IsGroup: true,
	})
	if err != nil {
		t.Fatalf("bob in group: %v", err)
	}
	if rootAlice != rootBob {
		t.Fatalf("expected same root for same group across members; got %q and %q", rootAlice, rootBob)
	}
}

// TestConversationSandbox_PathTraversalBlocked verifies that a hostile
// ExternalID or GroupID containing ../, absolute paths, or separators
// cannot escape its subtree. The strict allowlist in sanitizeDirName
// rejects these before any path is constructed.
func TestConversationSandbox_PathTraversalBlocked(t *testing.T) {
	base := t.TempDir()
	cfg := convCfg(base)

	tests := []struct {
		name string
		ctx  gateway.MsgContext
	}{
		{name: "DM ../escape", ctx: gateway.MsgContext{Gateway: "telegram", ExternalID: "../etc", IsGroup: false}},
		{name: "DM absolute path", ctx: gateway.MsgContext{Gateway: "telegram", ExternalID: "/etc/passwd", IsGroup: false}},
		{name: "DM with slash", ctx: gateway.MsgContext{Gateway: "telegram", ExternalID: "a/b", IsGroup: false}},
		{name: "DM backslash", ctx: gateway.MsgContext{Gateway: "telegram", ExternalID: "a\\b", IsGroup: false}},
		{name: "DM dotdot only", ctx: gateway.MsgContext{Gateway: "telegram", ExternalID: "..", IsGroup: false}},
		{name: "DM null char", ctx: gateway.MsgContext{Gateway: "telegram", ExternalID: "a\x00b", IsGroup: false}},
		{name: "group ../escape", ctx: gateway.MsgContext{Gateway: "telegram", GroupID: "../etc", IsGroup: true}},
		{name: "group absolute", ctx: gateway.MsgContext{Gateway: "telegram", GroupID: "/etc", IsGroup: true}},
		{name: "group with slash", ctx: gateway.MsgContext{Gateway: "telegram", GroupID: "a/b", IsGroup: true}},
		{name: "hostile gateway slash", ctx: gateway.MsgContext{Gateway: "tel/egram", ExternalID: "alice", IsGroup: false}},
		{name: "hostile gateway traversal", ctx: gateway.MsgContext{Gateway: "../etc", ExternalID: "alice", IsGroup: false}},
		{name: "hostile gateway absolute", ctx: gateway.MsgContext{Gateway: "/telegram", ExternalID: "alice", IsGroup: false}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, err := computeEffectiveSandboxRoot(cfg, tc.ctx)
			if err == nil {
				// If it somehow succeeded, verify containment as defense-in-depth.
				rel, relErr := filepath.Rel(filepath.Clean(base), filepath.Clean(root))
				if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
					t.Fatalf("root %q escaped sandbox despite no error; rel=%q relErr=%v", root, rel, relErr)
				}
				return
			}
			// A rejection from sanitizeDirName is the expected outcome.
		})
	}
}

// TestConversationSandbox_EmptyIdentityRejected verifies that missing
// identity fields are rejected rather than falling back to a shared root.
func TestConversationSandbox_EmptyIdentityRejected(t *testing.T) {
	base := t.TempDir()
	cfg := convCfg(base)

	tests := []struct {
		name string
		ctx  gateway.MsgContext
	}{
		{name: "empty gateway DM", ctx: gateway.MsgContext{ExternalID: "alice", IsGroup: false}},
		{name: "empty externalID DM", ctx: gateway.MsgContext{Gateway: "telegram", IsGroup: false}},
		{name: "empty gateway group", ctx: gateway.MsgContext{GroupID: "fam", IsGroup: true}},
		{name: "empty groupID group", ctx: gateway.MsgContext{Gateway: "telegram", IsGroup: true}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := computeEffectiveSandboxRoot(cfg, tc.ctx)
			if err == nil {
				t.Fatalf("expected error for empty identity, got nil")
			}
		})
	}
}

// TestFileIsolation_AcrossDMs verifies at the handleFileRead/Write level
// that two different DMs cannot read each other's files. This is the
// acceptance test: "Two different DMs get different sandbox roots; neither
// can read the other's file."
func TestFileIsolation_AcrossDMs(t *testing.T) {
	base := t.TempDir()
	cfg := convCfg(base)
	eval, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatalf("evaluator: %v", err)
	}

	// Agent for DM with Alice.
	rootAlice, err := computeEffectiveSandboxRoot(cfg, gateway.MsgContext{
		Gateway: "telegram", ExternalID: "alice", IsGroup: false,
	})
	if err != nil {
		t.Fatalf("alice root: %v", err)
	}
	os.MkdirAll(rootAlice, 0o700)
	agentAlice := &Agent{
		user:                 &cfg.Users[0],
		cfg:                  cfg,
		evaluator:            eval,
		classifier:           classifier.New(),
		effectiveSandboxRoot: rootAlice,
	}

	// Agent for DM with Bob.
	rootBob, err := computeEffectiveSandboxRoot(cfg, gateway.MsgContext{
		Gateway: "telegram", ExternalID: "bob", IsGroup: false,
	})
	if err != nil {
		t.Fatalf("bob root: %v", err)
	}
	os.MkdirAll(rootBob, 0o700)
	agentBob := &Agent{
		user:                 &cfg.Users[0],
		cfg:                  cfg,
		evaluator:            eval,
		classifier:           classifier.New(),
		effectiveSandboxRoot: rootBob,
	}

	// Alice writes a file.
	_, err = agentAlice.handleFileWrite(context.Background(), map[string]any{
		"path":    "secret.txt",
		"content": "alice's secret",
	})
	if err != nil {
		t.Fatalf("alice write: %v", err)
	}
	// Confirm it exists in Alice's sandbox.
	if _, err := os.Stat(filepath.Join(rootAlice, "secret.txt")); err != nil {
		t.Fatalf("alice file not found: %v", err)
	}

	// Bob attempts to read the same path — should fail because his
	// sandbox root is different and confinePath contains him to Bob's dir.
	_, err = agentBob.handleFileRead(context.Background(), map[string]any{
		"path": "secret.txt",
	})
	if err == nil {
		t.Fatalf("bob should not be able to read alice's file")
	}
}

// TestFamilyFacts_GlobalAcrossConversations is the regression guard:
// a fact proposed in one DM must be visible in another DM. Family facts
// (and user memories) are stored at the DB level keyed by family/user,
// NOT by conversation — the sandbox partitioning only affects the file_*
// tools.
func TestFamilyFacts_GlobalAcrossConversations(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fs := familystate.NewStore(db)

	cfg := &config.Config{Users: []config.UserConfig{
		{Name: "dep", Role: "parent"},
		{Name: "teo", DisplayName: "Teo", Role: "child", AgeGroup: "age_13_17"},
	}}

	// Two agents representing different DM conversations, both sharing
	// the same DB and family-state store.
	rootAlice, _ := computeEffectiveSandboxRoot(convCfg(t.TempDir()), gateway.MsgContext{
		Gateway: "telegram", ExternalID: "alice", IsGroup: false,
	})
	rootBob, _ := computeEffectiveSandboxRoot(convCfg(t.TempDir()), gateway.MsgContext{
		Gateway: "telegram", ExternalID: "bob", IsGroup: false,
	})
	os.MkdirAll(rootAlice, 0o700)
	os.MkdirAll(rootBob, 0o700)

	agentAlice := &Agent{
		user:                 &cfg.Users[0],
		cfg:                  cfg,
		familyState:          fs,
		effectiveSandboxRoot: rootAlice,
	}
	agentBob := &Agent{
		user:                 &cfg.Users[1],
		cfg:                  cfg,
		familyState:          fs,
		effectiveSandboxRoot: rootBob,
	}

	// Alice proposes a family fact (parent auto-apply path).
	_, err = agentAlice.handleProposeFamilyFact(context.Background(), map[string]any{
		"category": "pets", "subject": "family", "label": "Stella", "value": "cat",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	// Bob reads family state — the fact must be visible despite being in
	// a different DM conversation.
	out, err := agentBob.handleGetFamilyState(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("get family state: %v", err)
	}
	if !strings.Contains(out, "Stella") {
		t.Fatalf("family fact not visible across DMs; output: %q", out)
	}
}

// TestMigrateSandbox_MovesLegacyFiles verifies that legacy per-user/per-group
// files and loose files are relocated into conversations/_legacy/.
func TestMigrateSandbox_MovesLegacyFiles(t *testing.T) {
	base := t.TempDir()

	// Create legacy per-user layout.
	mustMkdir(t, filepath.Join(base, "users", "alice"))
	mustWrite(t, filepath.Join(base, "users", "alice", "diary.txt"), "alice's notes")

	// Create legacy per-group layout.
	mustMkdir(t, filepath.Join(base, "groups", "family"))
	mustWrite(t, filepath.Join(base, "groups", "family", "shopping.txt"), "milk")

	// Create a loose file in the flat root.
	mustWrite(t, filepath.Join(base, "readme.txt"), "hello")

	if err := migrateSandboxIfNecessary(base); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Legacy users/ files should be gone from the flat root.
	if _, err := os.Stat(filepath.Join(base, "users", "alice", "diary.txt")); !os.IsNotExist(err) {
		t.Errorf("legacy users/ file still in flat root: %v", err)
	}
	// They should be under conversations/_legacy/.
	legacyFile := filepath.Join(base, "conversations", "_legacy", "users", "alice", "diary.txt")
	if _, err := os.Stat(legacyFile); err != nil {
		t.Errorf("legacy file not migrated to %s: %v", legacyFile, err)
	}
	body, _ := os.ReadFile(legacyFile)
	if string(body) != "alice's notes" {
		t.Errorf("migrated file content wrong: %q", body)
	}

	// Legacy groups/ files.
	legacyGroupFile := filepath.Join(base, "conversations", "_legacy", "groups", "family", "shopping.txt")
	if _, err := os.Stat(legacyGroupFile); err != nil {
		t.Errorf("legacy group file not migrated: %v", err)
	}

	// Loose file.
	legacyLoose := filepath.Join(base, "conversations", "_legacy", "root", "readme.txt")
	if _, err := os.Stat(legacyLoose); err != nil {
		t.Errorf("loose file not migrated: %v", err)
	}

	// Marker file should exist.
	if _, err := os.Stat(filepath.Join(base, ".sandbox_migrated")); err != nil {
		t.Errorf("marker file not created: %v", err)
	}
}

// TestMigrateSandbox_Idempotent verifies that calling migration twice is
// a no-op (marker file prevents re-runs).
func TestMigrateSandbox_Idempotent(t *testing.T) {
	base := t.TempDir()
	mustWrite(t, filepath.Join(base, "loose.txt"), "data")

	if err := migrateSandboxIfNecessary(base); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// File should be moved to legacy.
	if _, err := os.Stat(filepath.Join(base, "loose.txt")); !os.IsNotExist(err) {
		t.Fatalf("loose file still in flat root after first migration")
	}

	// Second call should be a no-op.
	if err := migrateSandboxIfNecessary(base); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// TestMigrateSandbox_NoopOnEmpty verifies migration is a no-op when the
// sandbox root has no legacy files.
func TestMigrateSandbox_NoopOnEmpty(t *testing.T) {
	base := t.TempDir()
	if err := migrateSandboxIfNecessary(base); err != nil {
		t.Fatalf("migrate on empty: %v", err)
	}
	// Marker should exist.
	if _, err := os.Stat(filepath.Join(base, ".sandbox_migrated")); err != nil {
		t.Errorf("marker not created: %v", err)
	}
	// No legacy dir should exist.
	if _, err := os.Stat(filepath.Join(base, "conversations", "_legacy")); !os.IsNotExist(err) {
		t.Errorf("legacy dir should not exist on empty: %v", err)
	}
}
