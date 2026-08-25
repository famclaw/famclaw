package filesend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/gateway"
)

// fakeSender records the last delivery and optionally fails.
type fakeSender struct {
	destination gateway.OutboundDestination
	file        gateway.OutboundFile
	caption     string
	calls       int
	err         error
}

func (f *fakeSender) SendFile(ctx context.Context, dest gateway.OutboundDestination, file gateway.OutboundFile, caption string) error {
	f.calls++
	f.destination = dest
	f.file = file
	f.caption = caption
	return f.err
}

// fakeDB records audit entries.
type fakeDB struct {
	actors   []string
	gateways []string
	tools    []string
	args     [][]byte
	auditErr error
}

func (f *fakeDB) LogAudit(ctx context.Context, actorName, gw, toolName string, args []byte) error {
	if f.auditErr != nil {
		return f.auditErr
	}
	f.actors = append(f.actors, actorName)
	f.gateways = append(f.gateways, gw)
	f.tools = append(f.tools, toolName)
	f.args = append(f.args, args)
	return nil
}

// newSandbox writes content under a fresh temp conversation sandbox and
// returns the sandbox root and the file's relative path.
func newSandbox(t *testing.T, relPath, content string) (string, string) {
	t.Helper()
	root := t.TempDir()
	full := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return root, relPath
}

func TestConfinePath(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	// ConfinePath resolves symlinks on the full path, so the fixture files
	// must exist (same contract as file_read: it only addresses real files).
	for _, fixture := range []string{"report.md", filepath.Join("sub", "nested.txt"), filepath.Join("sub", "ok.txt")} {
		if err := os.WriteFile(filepath.Join(root, fixture), []byte("x"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	// Sibling-prefix fixture: a directory whose name has the sandbox root's
	// base name as a string prefix ("001" vs "0012"). A naive
	// strings.HasPrefix containment check would wrongly accept paths under
	// it; the filepath.Rel boundary check must reject them. The sibling
	// file must exist so the rejection is attributable to the Rel check
	// and not to EvalSymlinks failing on a missing path.
	sibling := filepath.Join(filepath.Dir(root), filepath.Base(root)+"2")
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatalf("mkdir sibling: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "evil"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write sibling fixture: %v", err)
	}

	outside := t.TempDir()

	tests := []struct {
		name        string
		root        string
		path        string
		wantErr     bool
		wantErrText string
		wantSuffix  string
	}{
		{name: "relative inside root", root: root, path: "report.md", wantSuffix: "report.md"},
		{name: "relative in subdir", root: root, path: "sub/nested.txt", wantSuffix: "sub/nested.txt"},
		{name: "dot-dot escape", root: root, path: "../outside.txt", wantErr: true},
		{name: "dot-dot-dot escape", root: root, path: "a/../../b", wantErr: true},
		{name: "absolute outside", root: root, path: filepath.Join(outside, "x.txt"), wantErr: true},
		{name: "absolute inside", root: root, path: filepath.Join(sub, "ok.txt"), wantSuffix: "ok.txt"},
		{name: "empty root", root: "", path: "report.md", wantErr: true},
		{name: "nonexistent relative", root: root, path: "nope.md", wantErr: true},
		{name: "sibling prefix trick", root: root, path: "../" + filepath.Base(root) + "2/evil", wantErr: true, wantErrText: "escapes sandbox root"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ConfinePath(tc.root, tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ConfinePath(%q, %q) = %q, want error", tc.root, tc.path, got)
				}
				if tc.wantErrText != "" && !strings.Contains(err.Error(), tc.wantErrText) {
					t.Fatalf("ConfinePath error = %q, want attributable to %q (not a missing-file lstat)", err, tc.wantErrText)
				}
				return
			}
			if err != nil {
				t.Fatalf("ConfinePath(%q, %q) error: %v", tc.root, tc.path, err)
			}
			if !strings.HasSuffix(got, tc.wantSuffix) {
				t.Errorf("ConfinePath suffix = %q, want %q", got, tc.wantSuffix)
			}
		})
	}
}

func TestConfinePathSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir() // outside the sandbox
	link := filepath.Join(root, "sneaky")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := ConfinePath(root, "sneaky/file.txt"); err == nil {
		t.Error("symlink escaping the sandbox was not rejected")
	}
}

func TestHandle(t *testing.T) {
	senders := map[string]gateway.FileSender{"discord": &fakeSender{}}
	sender := senders["discord"].(*fakeSender)
	db := &fakeDB{}

	oversized := make([]byte, MaxFileBytes+1)
	oversizedRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(oversizedRoot, "big.bin"), oversized, 0o600); err != nil {
		t.Fatalf("write oversized: %v", err)
	}
	// Traversal fixture: sandbox root with a secret file one level up.
	travBase := t.TempDir()
	travRoot := filepath.Join(travBase, "sandbox")
	if err := os.MkdirAll(travRoot, 0o700); err != nil {
		t.Fatalf("mkdir trav root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(travBase, "secret.txt"), []byte("top secret"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	dirRoot, _ := newSandbox(t, "docs/README.txt", "x") // dirRoot has a docs/ dir

	// Boundary fixture: a file of exactly MaxFileBytes passes the cap check
	// (">" rejects, "==" delivers) — pins the limit on both the stat-time
	// check and the post-read check on the delivered bytes.
	exactRoot := t.TempDir()
	exact := make([]byte, MaxFileBytes)
	if err := os.WriteFile(filepath.Join(exactRoot, "exact.bin"), exact, 0o600); err != nil {
		t.Fatalf("write exact-size fixture: %v", err)
	}
	groupDest := gateway.OutboundDestination{ExternalID: "user-9", GroupID: "chan-1"}
	dmDest := gateway.OutboundDestination{ExternalID: "user-9"}

	tests := []struct {
		name          string
		deliveryGw    string
		dest          gateway.OutboundDestination
		sandboxRoot   string
		path          string
		caption       string
		senderErr     error
		wantErr       string
		wantCalls     int
		wantAudit     bool
		wantName      string
		wantCaption   string
		wantDestGroup string
	}{
		{
			name:        "group delivery success",
			deliveryGw:  "discord",
			dest:        groupDest,
			sandboxRoot: func() string { r, _ := newSandbox(t, "report.md", "hello report"); return r }(),
			path:        "report.md",
			caption:     "Here is the report",
			wantCalls:   1,
			wantAudit:   true,
			wantName:    "report.md",
			wantCaption: "Here is the report",
		},
		{
			name:          "dm delivery success",
			deliveryGw:    "discord",
			dest:          dmDest,
			sandboxRoot:   func() string { r, _ := newSandbox(t, "a/b.png", "png-bytes"); return r }(),
			path:          "a/b.png",
			wantCalls:     1,
			wantAudit:     true,
			wantName:      "b.png",
			wantDestGroup: "",
		},
		{
			name:        "empty path rejected",
			deliveryGw:  "discord",
			dest:        groupDest,
			sandboxRoot: dirRoot,
			path:        "   ",
			wantErr:     "requires a 'path' argument",
			wantCalls:   0,
		},
		{
			name:        "path traversal rejected",
			deliveryGw:  "discord",
			dest:        groupDest,
			sandboxRoot: travRoot,
			path:        "../secret.txt",
			wantErr:     "escapes sandbox",
			wantCalls:   0,
		},
		{
			name:        "missing file rejected",
			deliveryGw:  "discord",
			dest:        groupDest,
			sandboxRoot: dirRoot,
			path:        "ghost.md",
			wantErr:     "resolving send_file path",
			wantCalls:   0,
		},
		{
			name:        "directory rejected",
			deliveryGw:  "discord",
			dest:        groupDest,
			sandboxRoot: dirRoot,
			path:        "docs",
			wantErr:     "not a regular file",
			wantCalls:   0,
		},
		{
			name:        "oversize rejected",
			deliveryGw:  "discord",
			dest:        groupDest,
			sandboxRoot: oversizedRoot,
			path:        "big.bin",
			wantErr:     "above the",
			wantCalls:   0,
		},
		{
			name:        "file of exactly the cap is delivered",
			deliveryGw:  "discord",
			dest:        groupDest,
			sandboxRoot: exactRoot,
			path:        "exact.bin",
			wantCalls:   1,
			wantName:    "exact.bin",
		},
		{
			name:        "no sender for gateway",
			deliveryGw:  "telegram",
			dest:        groupDest,
			sandboxRoot: func() string { r, _ := newSandbox(t, "x.txt", "x"); return r }(),
			path:        "x.txt",
			wantErr:     "file delivery is not available",
			wantCalls:   0,
		},
		{
			name:        "oversized caption rejected before delivery",
			deliveryGw:  "discord",
			dest:        groupDest,
			sandboxRoot: func() string { r, _ := newSandbox(t, "z.txt", "z"); return r }(),
			path:        "z.txt",
			caption:     strings.Repeat("x", 2001),
			wantErr:     "above the 2000 character limit",
			wantCalls:   0,
		},
		{
			name:        "audit failure does not fail a successful delivery",
			deliveryGw:  "discord",
			dest:        groupDest,
			sandboxRoot: func() string { r, _ := newSandbox(t, "w.txt", "w"); return r }(),
			path:        "w.txt",
			wantCalls:   1,
			wantName:    "w.txt",
			wantAudit:   false, // auditErr set below => no row recorded
		},
		{
			name:        "sender error propagated",
			deliveryGw:  "discord",
			dest:        groupDest,
			sandboxRoot: func() string { r, _ := newSandbox(t, "y.txt", "y"); return r }(),
			path:        "y.txt",
			senderErr:   errors.New("rate limited"),
			wantErr:     "rate limited",
			wantCalls:   1,
			wantAudit:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sender.calls = 0
			sender.err = tc.senderErr
			db.args = nil
			db.auditErr = nil
			if tc.name == "audit failure does not fail a successful delivery" {
				db.auditErr = errors.New("db is down")
			}
			got, err := Handle(context.Background(), db, senders, "dep", "discord", tc.deliveryGw, tc.dest, tc.sandboxRoot, tc.path, tc.caption)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Handle = %q, want error containing %q", got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Handle error = %q, want containing %q", err, tc.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("Handle error: %v", err)
				}
				if !strings.Contains(got, tc.wantName) {
					t.Errorf("Handle confirmation = %q, want to mention %q", got, tc.wantName)
				}
			}
			if sender.calls != tc.wantCalls {
				t.Errorf("sender calls = %d, want %d", sender.calls, tc.wantCalls)
			}
			if tc.wantCalls == 1 && tc.wantErr == "" {
				if sender.destination != tc.dest {
					t.Errorf("destination = %+v, want %+v", sender.destination, tc.dest)
				}
				if sender.caption != tc.wantCaption {
					t.Errorf("caption = %q, want %q", sender.caption, tc.wantCaption)
				}
				if string(sender.file.Name) != tc.wantName {
					t.Errorf("file name = %q, want %q", sender.file.Name, tc.wantName)
				}
			}
			if tc.wantAudit {
				if len(db.args) != 1 {
					t.Fatalf("audit entries = %d, want 1", len(db.args))
				}
				audit := string(db.args[0])
				if !strings.Contains(audit, tc.wantName) || !strings.Contains(audit, `"bytes"`) {
					t.Errorf("audit args = %s, want file name + bytes", audit)
				}
			}
		})
	}
}

// TestHandleEmptySandboxRoot pins the fail-closed behaviour when the
// conversation sandbox is disabled: nothing may be delivered.
func TestHandleEmptySandboxRoot(t *testing.T) {
	senders := map[string]gateway.FileSender{"discord": &fakeSender{}}
	_, err := Handle(context.Background(), nil, senders, "dep", "discord", "discord",
		gateway.OutboundDestination{ExternalID: "u"}, "", "report.md", "")
	if err == nil {
		t.Fatal("Handle with empty sandbox root must fail")
	}
	if !strings.Contains(err.Error(), "sandbox root not configured") {
		t.Errorf("error = %q, want sandbox root not configured", err)
	}
}

// TestToolDefinition pins the tool schema so a typo in required args or the
// tool name cannot silently change what the LLM is told.
func TestToolDefinition(t *testing.T) {
	tool := Tool()
	if tool.Name != "builtin__send_file" {
		t.Errorf("tool name = %q", tool.Name)
	}
	if tool.Source != "builtin" {
		t.Errorf("tool source = %q", tool.Source)
	}
	req, _ := tool.InputSchema["required"]
	switch v := req.(type) {
	case []string:
		if len(v) != 1 || v[0] != "path" {
			t.Errorf("required args = %v, want [path]", v)
		}
	case []any:
		if len(v) != 1 || v[0] != "path" {
			t.Errorf("required args = %v, want [path]", v)
		}
	default:
		t.Errorf("required args = %#v, want a list containing path", req)
	}
	props, _ := tool.InputSchema["properties"].(map[string]any)
	if _, ok := props["caption"]; !ok {
		t.Error("caption property missing from schema")
	}
}
