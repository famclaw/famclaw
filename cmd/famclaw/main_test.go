package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateSandboxRoot covers the pre-Landlock validation extraction.
// The full applyLandlockRules path mutates the running process's
// filesystem rights, so we test the pure validation step instead — same
// assertions, no kernel-state pollution for the rest of the suite.
func TestValidateSandboxRoot(t *testing.T) {
	sandbox := t.TempDir()
	cases := []struct {
		name      string
		root      string
		wantErr   bool
		errSubstr string
		wantClean string
	}{
		{name: "empty", root: "", wantErr: true, errSubstr: "empty"},
		{name: "relative", root: "relative/dir", wantErr: true, errSubstr: "absolute"},
		// "." fails on the absolute check before reaching the "/" or "." check.
		{name: "dot", root: ".", wantErr: true, errSubstr: "absolute"},
		{name: "root dir", root: "/", wantErr: true, errSubstr: "\"/\" or \".\""},
		{name: "missing", root: "/nonexistent/famclaw-188-qodofix", wantErr: true, errSubstr: "does not exist"},
		{name: "regular file not dir", root: filepath.Join(sandbox, "afile"), wantErr: true, errSubstr: "not a directory"},
		{name: "valid absolute dir", root: sandbox, wantErr: false, wantClean: sandbox},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "regular file not dir" {
				if err := os.WriteFile(tc.root, []byte("x"), 0600); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			got, err := validateSandboxRoot(tc.root)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for root %q, got nil", tc.root)
				}
				if !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("error %q does not contain %q", err, tc.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantClean {
				t.Fatalf("cleaned path = %q, want %q", got, tc.wantClean)
			}
		})
	}
}
