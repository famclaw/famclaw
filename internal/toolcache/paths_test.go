package toolcache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultCacheDirIsAbsoluteAndFamclaw(t *testing.T) {
	dir, err := DefaultCacheDir(context.Background())
	if err != nil {
		t.Fatalf("DefaultCacheDir: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("expected absolute path, got %q", dir)
	}
	if !strings.Contains(dir, "famclaw") {
		t.Errorf("expected path to contain 'famclaw', got %q", dir)
	}
}

func TestDefaultCacheDirHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DefaultCacheDir(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestEnsureUserDirCreatesWithCorrectMode(t *testing.T) {
	tmp := t.TempDir()
	dir, err := EnsureUserDir(context.Background(), tmp, "alice")
	if err != nil {
		t.Fatalf("EnsureUserDir: %v", err)
	}
	if !strings.HasSuffix(dir, "alice") {
		t.Errorf("expected dir to end in 'alice', got %q", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Error("not a dir")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
		t.Errorf("perm = %o, want 0700", info.Mode().Perm())
	}
}

func TestEnsureUserDirIdempotent(t *testing.T) {
	tmp := t.TempDir()
	if _, err := EnsureUserDir(context.Background(), tmp, "bob"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := EnsureUserDir(context.Background(), tmp, "bob"); err != nil {
		t.Fatalf("second call should be idempotent: %v", err)
	}
}

func TestEnsureUserDirHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EnsureUserDir(ctx, t.TempDir(), "alice"); !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestSanitizeUserName(t *testing.T) {
	cases := map[string]string{
		"alice":        "alice",
		"alice bob":    "alice_bob",
		"alice/../etc": "alice____etc", // 4 non-alphanum chars: /, ., ., /
		"":             "_",
		"..":           "__",
		"a.b":          "a_b",
		"a-b":          "a-b",
		"123":          "123",
	}
	for in, want := range cases {
		got := sanitizeUserName(in)
		if got != want {
			t.Errorf("sanitizeUserName(%q) = %q, want %q", in, got, want)
		}
	}
}
