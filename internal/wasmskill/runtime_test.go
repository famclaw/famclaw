package wasmskill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	got, err := Run(context.Background(), fixture(t, "fs-escape.wasm"), &Config{SandboxRoot: root}, []byte("probe\n"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got.Stdout) != 6 || got.Stdout[0] != 0 || got.Stdout[1] == 0 || got.Stdout[2] == 0 || got.Stdout[3] != 1 || got.Stdout[4] != 'S' {
		t.Fatalf("filesystem probe bytes = %v; expected inside preopen readable and traversal/absolute paths denied", got.Stdout)
	}
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
