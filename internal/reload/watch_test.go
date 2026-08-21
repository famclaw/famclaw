package reload

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// expectEvent waits for one coalesced event from w.Events within the
// deadline; fails the test if none arrives.
func expectEvent(t *testing.T, w *ConfigWatcher, what string) {
	t.Helper()
	select {
	case <-w.Events():
	case <-time.After(10 * time.Second):
		t.Fatalf("no %s event delivered within 10s", what)
	}
}

func seedConfig(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("a: 1\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return path
}

func TestConfigWatcher_InPlaceWrite(t *testing.T) {
	path := seedConfig(t, t.TempDir())
	w, err := NewConfigWatcher(path, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if err := os.WriteFile(path, []byte("a: 2\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	expectEvent(t, w, "in-place write")
}

func TestConfigWatcher_AtomicRename(t *testing.T) {
	path := seedConfig(t, t.TempDir())
	w, err := NewConfigWatcher(path, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// Atomic replace: temp file + rename — what config.Save does.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte("a: 2\n"), 0o600); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename: %v", err)
	}
	expectEvent(t, w, "atomic rename")
}

// TestConfigWatcher_SecondAtomicRename verifies the re-add logic: after the
// first replacement the original inode watch is dead, so a second
// replacement must still be delivered.
func TestConfigWatcher_SecondAtomicRename(t *testing.T) {
	dir := t.TempDir()
	path := seedConfig(t, dir)
	w, err := NewConfigWatcher(path, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	rename := func(v string) {
		t.Helper()
		tmp := filepath.Join(dir, "tmp-"+v)
		if err := os.WriteFile(tmp, []byte(v), 0o600); err != nil {
			t.Fatalf("write tmp: %v", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatalf("rename: %v", err)
		}
	}

	rename("a: 2\n")
	expectEvent(t, w, "first atomic rename")
	rename("a: 3\n")
	expectEvent(t, w, "second atomic rename")
}

func TestConfigWatcher_BurstCoalesces(t *testing.T) {
	path := seedConfig(t, t.TempDir())
	w, err := NewConfigWatcher(path, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// Three writes well within the debounce window.
	for i := 2; i <= 4; i++ {
		if err := os.WriteFile(path, []byte("a: "+string(rune('0'+i))+"\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// Exactly one coalesced event may arrive within the window plus slack.
	expectEvent(t, w, "coalesced burst")

	// Drain: no second event for the same burst.
	select {
	case <-w.Events():
		t.Fatal("burst produced more than one coalesced event")
	case <-time.After(500 * time.Millisecond):
	}
}

func TestConfigWatcher_CloseIdempotent(t *testing.T) {
	path := seedConfig(t, t.TempDir())
	w, err := NewConfigWatcher(path, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
