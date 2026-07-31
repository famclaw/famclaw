// Per-conversation sandbox migration tests (findings from PR #303 review).
// These cover: collision-safe merge, drain loop for loose files, marker
// correctness on partial failure, and close-error propagation in the copy
// path. The existing sandbox_*_test.go cases are untouched.
package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateSandbox_SameNameAcrossUsersGroups verifies that legacy files
// sharing a relative name under users/ and groups/ are both preserved —
// they land in separate _legacy/users/ and _legacy/groups/ subtrees, so one
// never clobbers the other.
func TestMigrateSandbox_SameNameAcrossUsersGroups(t *testing.T) {
	base := t.TempDir()
	mustMkdir(t, filepath.Join(base, "users", "alice"))
	mustWrite(t, filepath.Join(base, "users", "alice", "report.txt"), "user-report")
	mustMkdir(t, filepath.Join(base, "groups", "family"))
	mustWrite(t, filepath.Join(base, "groups", "family", "report.txt"), "group-report")

	if err := migrateSandboxIfNecessary(base); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	uf := filepath.Join(base, "conversations", "_legacy", "users", "alice", "report.txt")
	gf := filepath.Join(base, "conversations", "_legacy", "groups", "family", "report.txt")
	ub, err := os.ReadFile(uf)
	if err != nil || string(ub) != "user-report" {
		t.Errorf("user report lost: %q (err %v)", ub, err)
	}
	gb, err := os.ReadFile(gf)
	if err != nil || string(gb) != "group-report" {
		t.Errorf("group report lost: %q (err %v)", gb, err)
	}
	// Originals must be gone from the shared root.
	if _, err := os.Stat(filepath.Join(base, "users", "alice", "report.txt")); !os.IsNotExist(err) {
		t.Errorf("legacy user file still in shared root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "groups", "family", "report.txt")); !os.IsNotExist(err) {
		t.Errorf("legacy group file still in shared root: %v", err)
	}
}

// TestMigrateSandbox_MergeRefusesDataLoss verifies that a re-run whose
// destination already holds a DIFFERENT version of the same file is refused
// rather than silently overwriting — nothing is destroyed.
func TestMigrateSandbox_MergeRefusesDataLoss(t *testing.T) {
	base := t.TempDir()
	// Legacy users source carrying the "new" version.
	mustMkdir(t, filepath.Join(base, "users", "alice"))
	mustWrite(t, filepath.Join(base, "users", "alice", "diary.txt"), "new-version")
	// Simulate a partial prior run that already copied a DIFFERENT version
	// into the legacy tree.
	legacy := filepath.Join(base, "conversations", "_legacy", "users", "alice")
	mustMkdir(t, legacy)
	mustWrite(t, filepath.Join(legacy, "diary.txt"), "old-version")

	err := migrateSandboxIfNecessary(base)
	if err == nil {
		t.Fatal("expected error for conflicting content, got nil")
	}
	// The existing legacy copy must be preserved — not overwritten.
	body, _ := os.ReadFile(filepath.Join(legacy, "diary.txt"))
	if string(body) != "old-version" {
		t.Errorf("legacy file was silently overwritten: %q", body)
	}
	// The source must still be present (not removed on a refused merge).
	body, _ = os.ReadFile(filepath.Join(base, "users", "alice", "diary.txt"))
	if string(body) != "new-version" {
		t.Errorf("source file was lost: %q", body)
	}
	// A refused migration must not report success via the marker.
	if _, statErr := os.Stat(filepath.Join(base, ".sandbox_migrated")); statErr == nil {
		t.Error("marker written despite refused merge")
	}
}

// TestMigrateSandbox_MergeSkipsIdentical proves that a re-run after a partial
// migration whose copied files are identical is idempotent — it does not
// refuse (which would block forever) and completes successfully.
func TestMigrateSandbox_MergeSkipsIdentical(t *testing.T) {
	base := t.TempDir()
	mustMkdir(t, filepath.Join(base, "users", "alice"))
	mustWrite(t, filepath.Join(base, "users", "alice", "diary.txt"), "shared")
	// Pre-populate the legacy tree with the identical content, mimicking a
	// partial run that already moved this file.
	legacy := filepath.Join(base, "conversations", "_legacy", "users", "alice")
	mustMkdir(t, legacy)
	mustWrite(t, filepath.Join(legacy, "diary.txt"), "shared")

	if err := migrateSandboxIfNecessary(base); err != nil {
		t.Fatalf("re-merge of identical data should succeed: %v", err)
	}
	// Source removed, legacy preserved, marker written.
	if _, err := os.Stat(filepath.Join(base, "users", "alice", "diary.txt")); !os.IsNotExist(err) {
		t.Errorf("source not removed after idempotent merge: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(legacy, "diary.txt"))
	if string(body) != "shared" {
		t.Errorf("legacy content changed: %q", body)
	}
	if _, err := os.Stat(filepath.Join(base, ".sandbox_migrated")); err != nil {
		t.Errorf("marker not written: %v", err)
	}
}

// TestMigrateSandbox_LooseFilesDrainRace verifies that a loose file created
// between a scan and its move is still relocated by the drain loop, rather
// than being left behind in the shared sandbox root.
func TestMigrateSandbox_LooseFilesDrainRace(t *testing.T) {
	base := t.TempDir()
	mustWrite(t, filepath.Join(base, "first.txt"), "1")
	legacyRoot := filepath.Join(base, "conversations", "_legacy")

	reads := 0
	fakeReadDir := func(dir string) ([]os.DirEntry, error) {
		reads++
		if reads == 2 {
			// A loose file appears after the first scan and before the
			// second — exactly the window the drain loop must close.
			mustWrite(t, filepath.Join(base, "second.txt"), "2")
		}
		return os.ReadDir(dir)
	}

	if err := moveLooseSandboxFiles(base, legacyRoot, fakeReadDir); err != nil {
		t.Fatalf("move loose: %v", err)
	}

	for _, name := range []string{"first.txt", "second.txt"} {
		p := filepath.Join(legacyRoot, "root", name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s relocated to legacy root: %v", name, err)
		}
	}
	// No loose file may remain in the shared root.
	for _, e := range mustReaddir(t, base) {
		if e.IsDir() || e.Name() == ".sandbox_migrated" {
			continue
		}
		t.Errorf("loose file %s left in shared sandbox root", e.Name())
	}
}

// TestMoveLooseSandboxFiles_MaxDrainPasses verifies that the drain loop
// terminates after maxDrainPasses iterations to prevent hanging on infinite
// file creation loops.
func TestMoveLooseSandboxFiles_MaxDrainPasses(t *testing.T) {
	base := t.TempDir()
	legacyRoot := filepath.Join(base, "conversations", "_legacy")

	// Create a fake readDir that always returns a new file on each call
	// to simulate continuous file creation (this would cause an infinite loop
	// without the pass limit).
	passCount := 0
	fakeReadDir := func(dir string) ([]os.DirEntry, error) {
		passCount++
		// Create a new file on each pass to simulate the problem
		if passCount <= 101 { // Allow up to 101 passes to trigger the error
			fileName := fmt.Sprintf("pass_%d.txt", passCount)
			mustWrite(t, filepath.Join(base, fileName), "test content")
		}
		// Return all files in the directory
		return os.ReadDir(dir)
	}

	err := moveLooseSandboxFiles(base, legacyRoot, fakeReadDir)
	if err == nil {
		t.Fatal("expected error due to max drain passes exceeded")
	}
	if !strings.Contains(err.Error(), "still had loose files after 100 passes") {
		t.Fatalf("expected max passes error, got: %v", err)
	}
}

func mustReaddir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	return es
}

// failingCloseWriter is a WriteCloser whose Close returns a configured error
// while Write always succeeds. It proves the copy path propagates a
// destination close failure rather than swallowing it.
type failingCloseWriter struct{ err error }

func (failingCloseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *failingCloseWriter) Close() error             { return w.err }

// TestCopyAndClose_PropagatesCloseError verifies that copyAndClose — which
// copyFile uses to write and close the destination handle — returns a failed
// close instead of silently dropping it. This is the close-error propagation
// demanded by the PR #303 review (finding 4): previously the writable handle
// was deferred and closed with its error ignored, risking silent data loss on
// a flush/sync failure.
func TestCopyAndClose_PropagatesCloseError(t *testing.T) {
	w := &failingCloseWriter{err: errors.New("flush failed")}
	err := copyAndClose(strings.NewReader("some data"), w)
	if err == nil {
		t.Fatal("expected error from close, got nil")
	}
	if !errors.Is(err, w.err) {
		t.Fatalf("expected wrapped close error, got: %v", err)
	}
	// A clean close must not be reported as an error.
	ok := &failingCloseWriter{err: nil}
	if err := copyAndClose(strings.NewReader("data"), ok); err != nil {
		t.Fatalf("expected nil for clean close, got: %v", err)
	}
}

// TestCopyAndClose_PropagatesCopyError verifies a write failure is surfaced
// (and the handle still closed) when the destination rejects data.
func TestCopyAndClose_PropagatesCopyError(t *testing.T) {
	w := &writeFailer{err: errors.New("write rejected")}
	err := copyAndClose(strings.NewReader("data"), w)
	if err == nil {
		t.Fatal("expected error from copy, got nil")
	}
	if !errors.Is(err, w.err) {
		t.Fatalf("expected wrapped copy error, got: %v", err)
	}
}

// writeFailer is a WriteCloser whose Write always fails.
type writeFailer struct{ err error }

func (w *writeFailer) Write(p []byte) (int, error) { return 0, w.err }
func (w *writeFailer) Close() error                { return nil }

// TestCopyFile_RoundTrip exercises copyFile end-to-end to confirm the refactor
// (explicit close via copyAndClose, partial-destination cleanup) did not
// regress the happy path or the destination mode preservation.
func TestCopyFile_RoundTrip(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits are not meaningful when running as root")
	}
	root := t.TempDir()
	src := filepath.Join(root, "src.txt")
	mustWrite(t, src, "payload")
	if err := os.Chmod(src, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	dst := filepath.Join(root, "dst.txt")
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "payload" {
		t.Errorf("content mismatch: %q", got)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if m := info.Mode().Perm(); m != 0o640 {
		t.Errorf("mode mismatch: got %#o want 0640", m)
	}
}