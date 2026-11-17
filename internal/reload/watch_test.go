package reload

import (
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fsnotify/fsnotify"
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

func TestConfigWatcher_BurstDeliversLatest(t *testing.T) {
	path := seedConfig(t, t.TempDir())
	w, err := NewConfigWatcher(path, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("NewConfigWatcher: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// Filesystem event timing depends on the OS and scheduler.
	for i := 2; i <= 4; i++ {
		if err := os.WriteFile(path, []byte("a: "+string(rune('0'+i))+"\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	expectEvent(t, w, "write burst")
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "a: 4\n" {
		t.Fatalf("latest config = %q, error = %v", got, err)
	}
}

func TestConfigWatcher_ControlledBursts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &ConfigWatcher{
			debounce: 150 * time.Millisecond,
			notify:   make(chan struct{}, 1), errors: make(chan error, 1),
			closed: make(chan struct{}), done: make(chan struct{}),
		}
		events := make(chan fsnotify.Event)
		errors := make(chan error)
		go w.loopChannels(func() <-chan fsnotify.Event { return events }, func() <-chan error { return errors })
		defer w.Close()
		send := func() {
			events <- fsnotify.Event{Op: fsnotify.Write}
			synctest.Wait()
		}
		noEvent := func() {
			t.Helper()
			select {
			case <-w.Events():
				t.Fatal("unexpected event")
			default:
			}
		}
		expect := func() {
			t.Helper()
			select {
			case <-w.Events():
			default:
				t.Fatal("expected event")
			}
		}
		send()
		time.Sleep(50 * time.Millisecond)
		send()
		time.Sleep(50 * time.Millisecond)
		send()
		time.Sleep(149 * time.Millisecond)
		synctest.Wait()
		noEvent()
		time.Sleep(time.Millisecond)
		synctest.Wait()
		expect()
		time.Sleep(150 * time.Millisecond)
		synctest.Wait()
		noEvent()

		// A later burst must produce its own notification.
		send()
		time.Sleep(150 * time.Millisecond)
		synctest.Wait()
		expect()
		synctest.Wait()
		noEvent()
	})
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
