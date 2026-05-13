package toolcache

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// writePayload writes payload to <root>/<user>/<id>.bin with 0600 mode.
// Returns the relative path (forward slashes for sqlite portability). ctx
// is forwarded to EnsureUserDir so callers can cancel before the first
// filesystem syscall.
func writePayload(ctx context.Context, root, user, id string, payload []byte) (string, error) {
	userDir, err := EnsureUserDir(ctx, root, user)
	if err != nil {
		return "", fmt.Errorf("ensure user dir: %w", err)
	}
	absPath := filepath.Join(userDir, id+".bin")
	relPath := filepath.ToSlash(filepath.Join(sanitizeUserName(user), id+".bin"))

	f, err := os.OpenFile(absPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(payload); err != nil {
		return "", fmt.Errorf("write payload: %w", err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("sync file: %w", err)
	}

	return relPath, nil
}

// readPayload reads (offset, length) bytes from <root>/<rel>. Clamps
// negative offset/length to 0. Returns bytes actually read (may be < length
// on EOF). EOF / ErrUnexpectedEOF are not treated as errors — short reads
// are normal at end of file.
func readPayload(root, rel string, offset, length int) ([]byte, error) {
	if offset < 0 {
		offset = 0
	}
	if length < 0 {
		length = 0
	}

	absPath := filepath.Join(root, filepath.FromSlash(rel))
	f, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(int64(offset), io.SeekStart); err != nil {
			return nil, fmt.Errorf("seek file: %w", err)
		}
	}

	buf := make([]byte, length)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, fmt.Errorf("read file: %w", err)
	}

	return buf[:n], nil
}

// deletePayload removes the file at <root>/<rel>. Idempotent — returns nil
// if file already gone.
func deletePayload(root, rel string) error {
	absPath := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.Remove(absPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("remove file: %w", err)
		}
	}
	return nil
}

// statPayload returns the byte size of <root>/<rel>. Returns 0 + error if
// the file doesn't exist or is inaccessible.
func statPayload(root, rel string) (int64, error) {
	absPath := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(absPath)
	if err != nil {
		return 0, fmt.Errorf("stat file: %w", err)
	}
	return info.Size(), nil
}
