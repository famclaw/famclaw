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

// TestPrepareSandboxRoot tests the sandbox root preparation logic including
// directory creation when missing.
func TestPrepareSandboxRoot(t *testing.T) {
	sandbox := t.TempDir()
	cases := []struct {
		name      string
		root      string
		setupFunc func(string) error // setup to run before testing
		wantErr   bool
		errSubstr string
		// If wantDirMode is true, we check that the resulting path is a directory with mode 0700
		wantDirMode bool
	}{
		{name: "empty", root: "", setupFunc: nil, wantErr: true, errSubstr: "invalid sandbox root"},
		{name: "relative", root: "relative/dir", setupFunc: nil, wantErr: true, errSubstr: "invalid sandbox root"},
		{name: "dot", root: ".", setupFunc: nil, wantErr: true, errSubstr: "invalid sandbox root"},
		{name: "root dir", root: "/", setupFunc: nil, wantErr: true, errSubstr: "sandbox root must not be the root directory"},
		{name: "missing absolute", root: filepath.Join(sandbox, "missing"), setupFunc: nil, wantErr: false, wantDirMode: true},
		{name: "missing nested", root: filepath.Join(sandbox, "deep", "nested", "dir"), setupFunc: nil, wantErr: false, wantDirMode: true},
		{name: "existing dir", root: sandbox, setupFunc: nil, wantErr: false, wantDirMode: true},
		{name: "existing file", root: filepath.Join(sandbox, "file"), setupFunc: func(path string) error {
			return os.WriteFile(path, []byte("content"), 0600)
		}, wantErr: true, errSubstr: "not a directory"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup
			if tc.setupFunc != nil {
				if err := tc.setupFunc(tc.root); err != nil {
					t.Fatalf("setup failed: %v", err)
				}
			}

			// Execute
			got, err := prepareSandboxRoot(tc.root)

			// Check expectations
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for root %q, got nil", tc.root)
				}
				if !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("error %q does not contain %q", err, tc.errSubstr)
				}
				return
			}

			// No error expected
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Check that the path is a directory with correct permissions if requested
			if tc.wantDirMode {
				info, err := os.Stat(got)
				if err != nil {
					t.Fatalf("failed to stat resulting path %q: %v", got, err)
				}
				if !info.IsDir() {
					t.Fatalf("resulting path %q is not a directory", got)
				}
				if info.Mode()&os.ModePerm != 0o700 {
					t.Fatalf("resulting path %q has mode %o, expected 0o700", got, info.Mode()&os.ModePerm)
				}
			}
		})
	}
}
