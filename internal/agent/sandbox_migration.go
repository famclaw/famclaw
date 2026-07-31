// Package agent — sandbox migration logic.
//
// MigrateSandbox relocates files left over from the shelved per-user/per-group
// layout (issue #221) and any loose files in the flat sandbox root into
// conversations/_legacy/ so they remain readable but do not collide with
// per-conversation subdirectories.
package agent

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// MigrateSandbox relocates files left over from the shelved per-user/per-group
// layout (issue #221) and any loose files in the flat sandbox root into
// conversations/_legacy/. It is idempotent.
func MigrateSandbox(base string) error {
	return migrateSandboxIfNecessary(base)
}

// migrateSandboxIfNecessary relocates files left over from the shelved
// per-user/per-group layout (issue #221) and any loose files in the flat
// sandbox root into conversations/_legacy/. It is idempotent: a marker file
// (.sandbox_migrated) records a completed run.
//
// Migration strategy: legacy files are preserved in-place under
// base/conversations/_legacy/ rather than being silently orphaned. The old
// per-user and per-group subdirectories are flattened into
// _legacy/users/ and _legacy/groups/ respectively.
//
// Safe partial failure: if any step errors, the error is returned and the
// marker is NOT written, so the next startup retries. Existing copies are
// never silently overwritten — a content conflict is refused rather than
// destroying data.
func migrateSandboxIfNecessary(base string) error {
	if base == "" {
		return nil
	}
	baseClean := filepath.Clean(base)
	marker := filepath.Join(baseClean, ".sandbox_migrated")
	if _, err := os.Stat(marker); err == nil {
		return nil // already migrated (marker written only on successful completion)
	}
	legacyRoot := filepath.Join(baseClean, "conversations", "_legacy")
	// Migrate users/ and groups/ from the old per-user/per-group layout.
	for _, sub := range []string{"users", "groups"} {
		if err := moveSandboxSubdir(baseClean, sub, filepath.Join(legacyRoot, sub)); err != nil {
			return fmt.Errorf("migrating %s sandbox: %w", sub, err)
		}
	}
	// Migrate loose files in the flat root (regular files only).
	if err := moveLooseSandboxFiles(baseClean, legacyRoot, os.ReadDir); err != nil {
		return fmt.Errorf("migrating loose sandbox files: %w", err)
	}
	if err := os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)), 0o600); err != nil {
		return fmt.Errorf("writing migration marker: %w", err)
	}
	return nil
}

// moveSandboxSubdir relocates a top-level subdirectory of the sandbox root
// into the legacy tree. Uses os.Rename for same-filesystem atomicity; falls
// back to a recursive copy+delete for cross-device moves.
//
// When the destination already exists and is non-empty (e.g. a re-run after
// a partial migration), the source is merged into the destination with
// mergeDir, which refuses to silently overwrite a file whose contents differ
// from the source. Identical files are skipped so the re-run is idempotent.
func moveSandboxSubdir(base, name, dst string) error {
	oldPath := filepath.Join(base, name)
	info, err := os.Stat(oldPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to migrate
		}
		return fmt.Errorf("stating %s: %w", oldPath, err)
	}
	if !info.IsDir() {
		return nil // skip non-directories with these names
	}
	// If the destination already exists and is non-empty, merge contents.
	dstInfo, dstErr := os.Stat(dst)
	if dstErr == nil && dstInfo.IsDir() && !isEmptyDir(dst) {
		if err := mergeDir(oldPath, dst); err != nil {
			return fmt.Errorf("merging %s: %w", name, err)
		}
		// RemoveAll the now-merged source. If it fails, the originals
		// remain in the old location: the error is returned and the marker
		// is NOT written, so the next startup retries the merge. Leaving
		// both copies on a partial failure is the safe choice — it never
		// looks like success.
		if err := os.RemoveAll(oldPath); err != nil {
			return fmt.Errorf("copy succeeded, but removing original after merge for %s failed: both source and destination hold files; migration will retry on next startup: %w", name, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("mkdir parent %s: %w", filepath.Dir(dst), err)
	}
	if err := os.Rename(oldPath, dst); err == nil {
		return nil
	}
	// Cross-device — fall back to recursive copy then delete. If the
	// RemoveAll fails, stale data remains in the old location: the error
	// is returned and the marker is not written, so the next startup
	// retries.
	if err := copyDir(oldPath, dst); err != nil {
		return fmt.Errorf("copying %s: %w", name, err)
	}
	if err := os.RemoveAll(oldPath); err != nil {
		return fmt.Errorf("removing original after copy for %s: %w", name, err)
	}
	return nil
}

// moveLooseSandboxFiles relocates regular files sitting directly in the
// sandbox root (the pre-#221 flat layout) into _legacy/root/. Directories are
// skipped so their migration is handled by moveSandboxSubdir.
//
// The legacy directory is only created when at least one loose file exists,
// so an empty sandbox root leaves no artifacts behind.
//
// readDir is injected to make the drain loop testable without global state:
// callers pass os.ReadDir; tests pass a fake that simulates a file appearing
// between scans.
func moveLooseSandboxFiles(base, legacyRoot string, readDir func(string) ([]os.DirEntry, error)) error {
	legacyPath := filepath.Join(legacyRoot, "root")
	// Drain loose files in repeated passes. A single ReadDir snapshot can
	// miss a file created between that snapshot and the move; re-scanning
	// until a full pass moves nothing guarantees no loose file is left in
	// the shared sandbox root where every conversation could still read it.
	const maxDrainPasses = 100
	createdLegacy := false
	totalMoved := 0
	for pass := 0; ; pass++ {
		if pass >= maxDrainPasses {
			return fmt.Errorf("sandbox root %s still had loose files after %d passes (%d files moved total): new files may be continuously appearing", base, maxDrainPasses, totalMoved)
		}
		entries, err := readDir(base)
		if err != nil {
			return fmt.Errorf("reading sandbox root: %w", err)
		}
		moved := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if e.Name() == ".sandbox_migrated" {
				continue
			}
			if !createdLegacy {
				if err := os.MkdirAll(legacyPath, 0o700); err != nil {
					return fmt.Errorf("mkdir legacy root: %w", err)
				}
				createdLegacy = true
			}
			if err := moveLooseFile(base, e.Name(), legacyPath); err != nil {
				return fmt.Errorf("migrating loose file %s: %w", e.Name(), err)
			}
			moved++
		}
		if moved == 0 {
			return nil
		}
	}
}

// moveLooseFile relocates a single loose file from base into legacyPath.
// It prefers os.Rename for atomicity, but first checks that the destination
// is absent so a partial-prior-run retry cannot silently clobber an existing
// file with differing contents:
//   - destination absent: try an atomic rename, falling back to copy+delete
//     for cross-device moves.
//   - destination exists and identical: drop the source (the copy already
//     lives in the legacy tree).
//   - destination exists and differs: refuse (data-loss prevention).
func moveLooseFile(base, name, legacyPath string) error {
	oldPath := filepath.Join(base, name)
	dstPath := filepath.Join(legacyPath, name)
	if exists, err := fileExists(dstPath); err != nil {
		return fmt.Errorf("stating %s: %w", dstPath, err)
	} else if exists {
		same, err := sameFileContent(oldPath, dstPath)
		if err != nil {
			return fmt.Errorf("comparing %s: %w", name, err)
		}
		if !same {
			return fmt.Errorf("loose file %s would overwrite existing %s: refusing to lose data", name, dstPath)
		}
		// Identical content already present in the legacy tree; just
		// remove the source so it no longer lives in the shared root.
		if err := os.Remove(oldPath); err != nil {
			return fmt.Errorf("removing original for identical match %s: %w", name, err)
		}
		return nil
	}
	// Destination absent — try atomic rename.
	if err := os.Rename(oldPath, dstPath); err == nil {
		return nil
	}
	// Cross-device — fall back to copy then delete.
	if err := copyFile(oldPath, dstPath); err != nil {
		return fmt.Errorf("copying %s: %w", name, err)
	}
	if err := os.Remove(oldPath); err != nil {
		return fmt.Errorf("removing original after copy for %s: %w", name, err)
	}
	return nil
}

// isEmptyDir reports whether dir contains no entries.
func isEmptyDir(dir string) bool {
	dir = filepath.Clean(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	return len(entries) == 0
}

// copyDir recursively copies src into dst, creating directories as needed.
// It overwrites files that already exist at the destination and is intended
// for fresh copies where no collision is expected; use mergeDir for
// collision-safe merges.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyFile(path, target)
	})
}

// mergeDir copies src into dst, preserving any files already present in dst.
// A destination file that differs from its source counterpart is never
// silently overwritten: the merge stops and returns a collision error so
// that no data is lost. Identical files are skipped, making the operation
// safe to re-run after a partial migration.
func mergeDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if exists, err := fileExists(target); err != nil {
			return fmt.Errorf("checking %s: %w", rel, err)
		} else if exists {
			same, err := sameFileContent(path, target)
			if err != nil {
				return fmt.Errorf("comparing %s: %w", rel, err)
			}
			if !same {
				return fmt.Errorf("sandbox merge would overwrite %s: refusing to lose data", rel)
			}
			return nil // identical — safe to skip
		}
		return copyFile(path, target)
	})
}

// fileExists reports whether path exists (regardless of whether it is a
// file or directory).
func fileExists(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
}

// sameFileContent reports whether the two paths hold identical bytes.
// Sizes are compared first as a fast path; matching-size contents are then
// streamed and compared.
func sameFileContent(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	if ai.Size() != bi.Size() {
		return false, nil
	}
	af, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer func() { _ = af.Close() }()
	bf, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer func() { _ = bf.Close() }()
	ba, err := io.ReadAll(af)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", a, err)
	}
	bb, err := io.ReadAll(bf)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", b, err)
	}
	return bytes.Equal(ba, bb), nil
}

// copyFile copies a regular file, preserving its mode. The destination is
// removed on any error so a failed copy never leaves a partial file behind.
// The writable handle is closed explicitly via copyAndClose, which propagates
// a close failure instead of swallowing it (see PR #303 review).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stating %s: %w", src, err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	if err := copyAndClose(in, out); err != nil {
		_ = os.Remove(dst) // never leave a partial destination
		return fmt.Errorf("copying %s: %w", dst, err)
	}
	return nil
}

// copyAndClose copies r into w until EOF, then closes w. A failure to close
// the writable handle is returned rather than swallowed: unlike a plain
// `defer w.Close()`, a failed close can signal a flush/sync error that would
// otherwise lose data. A copy failure closes w (discarding its error) and
// returns the copy error.
func copyAndClose(r io.Reader, w io.WriteCloser) error {
	if _, err := io.Copy(w, r); err != nil {
		_ = w.Close()
		return fmt.Errorf("copy: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}
