// Package agent — sandbox migration logic.
//
// MigrateSandbox relocates files left over from the shelved per-user/per-group
// layout (issue #221) and any loose files in the flat sandbox root into
// conversations/_legacy/ so they remain readable but do not collide with
// per-conversation subdirectories.
package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// MigrateSandbox relocates files left over from the shelved
// per-user/per-group layout (issue #221) and any loose files in the flat
// sandbox root into conversations/_legacy/ so they remain readable but do
// not collide with per-conversation subdirectories. It is idempotent.
func MigrateSandbox(base string) error {
	return migrateSandboxIfNecessary(base)
}

// migrateSandboxIfNecessary relocates files left over from the shelved
// per-user/per-group layout (issue #221) and any loose files in the flat
// sandbox root into conversations/_legacy/ so they remain readable but do
// not collide with per-conversation subdirectories. It is idempotent — a
// marker file (.sandbox_migrated) records a completed run.
//
// Migration strategy: legacy files are preserved in-place under
// base/conversations/_legacy/ rather than being silently orphaned. The old
// per-user and per-group subdirectories are flattened into
// _legacy/users/ and _legacy/groups/ respectively.
func migrateSandboxIfNecessary(base string) error {
	if base == "" {
		return nil
	}
	baseClean := filepath.Clean(base)
	marker := filepath.Join(baseClean, ".sandbox_migrated")
	if _, err := os.Stat(marker); err == nil {
		return nil // already migrated
	}
	legacyRoot := filepath.Join(baseClean, "conversations", "_legacy")
	// Migrate users/ and groups/ from the old per-user/per-group layout.
	for _, sub := range []string{"users", "groups"} {
		if err := moveSandboxSubdir(baseClean, sub, filepath.Join(legacyRoot, sub)); err != nil {
			return fmt.Errorf("migrating %s sandbox: %w", sub, err)
		}
	}
	// Migrate loose files in the flat root (regular files only — never
	// directories other than the ones we just handled above).
	if err := moveLooseSandboxFiles(baseClean, legacyRoot); err != nil {
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
func moveSandboxSubdir(base, name, dst string) error {
	oldPath := filepath.Join(base, name)
	info, err := os.Stat(oldPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to migrate
		}
		return err
	}
	if !info.IsDir() {
		return nil // skip non-directories with these names
	}
	// If the destination already exists and is non-empty, merge contents
	// to be safe (e.g. re-run after a partial migration).
	dstInfo, dstErr := os.Stat(dst)
	if dstErr == nil && dstInfo.IsDir() && !isEmptyDir(dst) {
		return copyDir(oldPath, dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("mkdir parent %s: %w", filepath.Dir(dst), err)
	}
	if err := os.Rename(oldPath, dst); err == nil {
		return nil
	}
	// Cross-device — fall back to recursive copy then delete.
	if err := copyDir(oldPath, dst); err != nil {
		return fmt.Errorf("copying %s: %w", name, err)
	}
	return os.RemoveAll(oldPath)
}

// moveLooseSandboxFiles relocates regular files sitting directly in the
// sandbox root (the pre-#221 flat layout) into _legacy/root/. Directories
// are skipped so their migration is handled by moveSandboxSubdir.
// The legacy directory is only created when at least one loose file exists,
// so an empty sandbox root leaves no artifacts behind.
func moveLooseSandboxFiles(base, legacyRoot string) error {
	entries, err := os.ReadDir(base)
	if err != nil {
		return fmt.Errorf("reading sandbox root: %w", err)
	}
	// Scan for loose files before creating any destination directory.
	var hasLoose bool
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if e.Name() == ".sandbox_migrated" {
			continue
		}
		hasLoose = true
		break
	}
	if !hasLoose {
		return nil
	}
	legacyPath := filepath.Join(legacyRoot, "root")
	if err := os.MkdirAll(legacyPath, 0o700); err != nil {
		return fmt.Errorf("mkdir legacy root: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Skip the migration marker if it somehow survived.
		if e.Name() == ".sandbox_migrated" {
			continue
		}
		oldPath := filepath.Join(base, e.Name())
		dstPath := filepath.Join(legacyPath, e.Name())
		if err := os.Rename(oldPath, dstPath); err != nil {
			// Cross-device or collision — copy then delete.
			if err := copyFile(oldPath, dstPath); err != nil {
				return fmt.Errorf("migrating loose file %s: %w", e.Name(), err)
			}
			if err := os.Remove(oldPath); err != nil {
				_ = err // best-effort cleanup
			}
		}
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

// copyFile copies a regular file, preserving mode.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
