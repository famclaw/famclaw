package wasmskill

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestRunStdioAndExplicitStartOnce(t *testing.T) {
	input := []byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n")
	got, err := Run(context.Background(), fixture(t, "echo.wasm"), nil, input)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !bytes.Equal(got.Stdout, input) {
		t.Fatalf("stdout = %q, want exactly one echoed request %q", got.Stdout, input)
	}
	if got.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", got.ExitCode)
	}
}

func TestFilesystemDefaultDeny(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sandbox.txt"), []byte("S"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Run(context.Background(), fixture(t, "fs-escape.wasm"), nil, []byte("probe\n"))
	if err != nil {
		t.Fatalf("Run with no preopens: %v", err)
	}
	if len(got.Stdout) != 6 || got.Stdout[1] == 0 || got.Stdout[3] != 0 || got.Stdout[4] != 0 {
		t.Fatalf("filesystem probe bytes = %v; expected traversal denied and no sandbox data readable", got.Stdout)
	}
}

func TestSymlinkPreopenEscapeBehavior(t *testing.T) {
	wasm := fixture(t, "symlink-escape.wasm")
	t.Run("no preopen denies symlink operations", func(t *testing.T) {
		got, err := Run(context.Background(), wasm, nil, nil)
		if err != nil {
			t.Fatalf("Run without preopen: %v", err)
		}
		if len(got.Stdout) != 2 || got.Stdout[0] != 8 || got.Stdout[1] != 8 {
			t.Fatalf("errno bytes = %v, want both operations to fail with EBADF (8)", got.Stdout)
		}
	})
}

func TestNoNetworkForeignImportRejected(t *testing.T) {
	_, err := Run(context.Background(), fixture(t, "foreign-import.wasm"), nil, nil)
	if !errorsIs(err, ErrDisallowedImport) {
		t.Fatalf("error = %v, want ErrDisallowedImport", err)
	}
}

func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestPolicyRejectsNonWasmAndAdmitsChecksumArtifact(t *testing.T) {
	cases := []struct {
		name string
		ref  SkillRef
		want string
	}{
		{name: "native artifact refused", ref: SkillRef{Name: "native", Artifact: "native.bin"}, want: TierRefused},
		{name: "missing checksum refused", ref: SkillRef{Name: "skill.wasm", Artifact: "x.wasm"}, want: TierRefused},
		{name: "wasm admitted", ref: SkillRef{Name: "skill.wasm", Artifact: "x.wasm", Checksum: "sha256:" + strings.Repeat("a", 64)}, want: TierWasm},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Evaluate(tc.ref).Tier; got != tc.want {
				t.Fatalf("tier = %q, want %q", got, tc.want)
			}
		})
	}
	wasm := fixture(t, "simple.wasm")
	sum := sha256.Sum256(wasm)
	path := filepath.Join(t.TempDir(), "skill.wasm")
	if err := os.WriteFile(path, wasm, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadWasmArtifact(SkillRef{Name: "skill.wasm", Artifact: path, Checksum: "sha256:" + hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatalf("LoadWasmArtifact: %v", err)
	}
	if !bytes.Equal(loaded, wasm) {
		t.Fatal("loaded artifact differs")
	}
}

func TestRunContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := Run(ctx, fixture(t, "hang.wasm"), nil, nil)
	if err == nil {
		t.Fatal("expected context cancellation for non-terminating module")
	}
}

func TestReadLinePropagatesEOFForPartialResponse(t *testing.T) {
	guestExit := errors.New("guest exited")
	s := &Skill{out: bufio.NewReader(strings.NewReader(`{"jsonrpc":"2.0","id":1}`)), done: make(chan struct{}), startErr: guestExit}
	close(s.done)
	line, err := s.readLine(context.Background())
	if !errors.Is(err, io.EOF) || len(line) == 0 {
		t.Fatalf("readLine = %q, %v; want partial line and EOF", line, err)
	}
	if got := s.sessionErr(); !errors.Is(got, guestExit) {
		t.Fatalf("sessionErr = %v, want guest-exit cause %v", got, guestExit)
	}
}
