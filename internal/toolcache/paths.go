// Package toolcache implements the spillover cache for large tool results.
// See docs/superpowers/specs/2026-05-12-context-management-design.md.
package toolcache

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultCacheDir returns the platform-appropriate root cache directory.
//
//	Mac / Linux / Termux: $HOME/.famclaw/cache/tool-results
//	Windows:              %LOCALAPPDATA%\famclaw\cache\tool-results (falls
//	                      back to %APPDATA% if LOCALAPPDATA is unset)
func DefaultCacheDir() (string, error) {
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = os.Getenv("APPDATA")
		}
		if base == "" {
			return "", fmt.Errorf("toolcache: neither LOCALAPPDATA nor APPDATA set")
		}
		return filepath.Join(base, "famclaw", "cache", "tool-results"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("toolcache: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".famclaw", "cache", "tool-results"), nil
}

// EnsureUserDir creates (idempotently) the per-user subdir under root and
// returns its absolute path. On non-Windows, enforces mode 0700.
func EnsureUserDir(root, user string) (string, error) {
	clean := sanitizeUserName(user)
	dir := filepath.Join(root, clean)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("toolcache: mkdir %s: %w", dir, err)
	}
	if runtime.GOOS != "windows" {
		// MkdirAll honors umask — re-chmod to guarantee 0700.
		_ = os.Chmod(dir, 0700)
	}
	return dir, nil
}

// sanitizeUserName replaces filesystem-unsafe characters in user names.
// Defense in depth — user names are constrained by config.yaml validation,
// but never trust input that lands in a path. Returns "_" for empty input
// so we never end up writing to root.
func sanitizeUserName(name string) string {
	if name == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}
