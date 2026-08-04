package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
)

// TestInitMCPPool_NonFatalOnBadConfig tests that initMCPPool returns a pool
// (does not fatal-exit) when an MCP server is misconfigured or unreachable.
// This verifies the #199 fix: MCP pool initialization is non-fatal.
func TestInitMCPPool_NonFatalOnBadConfig(t *testing.T) {
	// Create a temp dir for sandbox root
	sandboxRoot := t.TempDir()

	// Config with a deliberately broken MCP server (nonexistent binary)
	cfg := &config.Config{
		Tools: config.ToolsConfig{
			SandboxRoot: sandboxRoot,
			Sandbox: config.SandboxConfig{
				Enabled: boolPtr(true),
			},
		},
		Skills: config.SkillsConfig{
			MCPServers: map[string]config.MCPServerConfig{
				"bad-server": {
					Transport: "stdio",
					Command:   "/nonexistent/binary/that/does/not/exist",
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, _, err := initMCPPool(ctx, cfg, sandboxRoot)

	// Should not return an error (non-fatal behavior)
	if err != nil {
		t.Fatalf("initMCPPool returned error (should be non-fatal): %v", err)
	}

	// Pool should be returned even though server failed to start
	if pool == nil {
		t.Fatal("initMCPPool returned nil pool")
	}

	// The bad server should not have any tools registered
	tools := pool.ListTools()
	if len(tools) != 0 {
		t.Errorf("expected 0 tools from failed server, got %d: %v", len(tools), tools)
	}

	// Pool should be usable (StopAll should not panic)
	pool.StopAll()
}

// TestInitMCPPool_SandboxEnabledFailClosed tests that when sandbox is enabled
// but kernel lacks support, the error is logged but non-fatal.
func TestInitMCPPool_SandboxEnabledFailClosed(t *testing.T) {
	sandboxRoot := t.TempDir()

	cfg := &config.Config{
		Tools: config.ToolsConfig{
			SandboxRoot: sandboxRoot,
			Sandbox: config.SandboxConfig{
				Enabled: boolPtr(true),
			},
		},
		Skills: config.SkillsConfig{
			MCPServers: map[string]config.MCPServerConfig{
				"test-server": {
					Transport: "stdio",
					Command:   "/bin/true", // simple command that exits
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// This will fail on kernels without landlock/seccomp, but should not fatal
	pool, _, err := initMCPPool(ctx, cfg, sandboxRoot)

	if err != nil {
		t.Fatalf("initMCPPool returned error (should be non-fatal): %v", err)
	}

	if pool == nil {
		t.Fatal("initMCPPool returned nil pool")
	}

	pool.StopAll()
}

// TestInitMCPPool_NoServers tests that initMCPPool works with no MCP servers configured.
func TestInitMCPPool_NoServers(t *testing.T) {
	sandboxRoot := t.TempDir()

	cfg := &config.Config{
		Tools: config.ToolsConfig{
			SandboxRoot: sandboxRoot,
			Sandbox: config.SandboxConfig{
				Enabled: boolPtr(false),
			},
		},
		Skills: config.SkillsConfig{
			MCPServers: map[string]config.MCPServerConfig{},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, _, err := initMCPPool(ctx, cfg, sandboxRoot)

	if err != nil {
		t.Fatalf("initMCPPool returned error: %v", err)
	}

	if pool == nil {
		t.Fatal("initMCPPool returned nil pool")
	}

	tools := pool.ListTools()
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d: %v", len(tools), tools)
	}

	pool.StopAll()
}

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

func boolPtr(b bool) *bool { return &b }

// TestCheckSearchEndpointReachable_EndpointWithPath verifies that the
// startup probe correctly joins a sub-path endpoint with /search, producing
// /searx/search (not /searx/search/search) so the reachability check does
// not always warn due to a double-path.
func TestCheckSearchEndpointReachable_EndpointWithPath(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	// Endpoint with a sub-path — the probe should hit /searx/search.
	endpoint := server.URL + "/searx"
	err := checkSearchEndpointReachable(endpoint)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedPath != "/searx/search" {
		t.Errorf("probe path = %q, want /searx/search (double-path would warn)", capturedPath)
	}
}

// TestCheckSearchEndpointReachable_Unreachable verifies that a connection
// refused produces an error (not nil), so the startup WARNING fires.
func TestCheckSearchEndpointReachable_Unreachable(t *testing.T) {
	err := checkSearchEndpointReachable("http://127.0.0.1:1")
	if err == nil {
		t.Fatal("expected error for unreachable endpoint, got nil")
	}
}

// TestCheckSearchEndpointReachable_StripsCredentials verifies that credentials
// embedded in the endpoint URL (e.g. http://user:pass@host) are NOT transmitted
// in the startup probe request. The probe should only check that the host is
// listening.
func TestCheckSearchEndpointReachable_StripsCredentials(t *testing.T) {
	var gotAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// r.URL.User is populated by Go's HTTP client when the request
		// URL contains userinfo. If credentials were sent, this will be
		// non-nil.
		gotAuth = r.URL.User != nil
	}))
	defer server.Close()

	// Parse the server URL and inject credentials.
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	parsed.User = url.UserPassword("user", "secret")
	endpoint := parsed.String()

	err = checkSearchEndpointReachable(endpoint)
	if err != nil {
		t.Fatalf("expected nil error for reachable endpoint, got %v", err)
	}
	if gotAuth {
		t.Errorf("probe request must not transmit credentials embedded in the endpoint URL")
	}
}

// TestCheckSearchEndpointReachable_CredentialedEndpointNoLeak verifies that
// a credentialed endpoint is logged via SanitizeEndpoint (no credentials in
// the warning/error message).
func TestCheckSearchEndpointReachable_CredentialedEndpointNoLeak(t *testing.T) {
	err := checkSearchEndpointReachable("http://user:pass@127.0.0.1:1")
	if err == nil {
		t.Fatal("expected error for unreachable endpoint, got nil")
	}
	if strings.Contains(err.Error(), "pass") {
		t.Errorf("error must not contain credentials: %v", err)
	}
}
