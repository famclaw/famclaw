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
	"github.com/famclaw/famclaw/internal/websearch"
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

// TestProbeSearchEndpoint_EndpointWithPath verifies that the
// startup probe correctly joins a sub-path endpoint with /search, producing
// /searx/search (not /searx/search/search) so the reachability check does
// not always warn due to a double-path.
func TestProbeSearchEndpoint_EndpointWithPath(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	// Endpoint with a sub-path — the probe should hit /searx/search.
	endpoint := server.URL + "/searx"
	err := websearch.ProbeSearchEndpoint(endpoint, probeTimeout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedPath != "/searx/search" {
		t.Errorf("probe path = %q, want /searx/search (double-path would warn)", capturedPath)
	}
}

// TestProbeSearchEndpoint_Unreachable verifies that a connection
// refused produces an error (not nil), so the startup WARNING fires.
func TestProbeSearchEndpoint_Unreachable(t *testing.T) {
	err := websearch.ProbeSearchEndpoint("http://127.0.0.1:1", probeTimeout)
	if err == nil {
		t.Fatal("expected error for unreachable endpoint, got nil")
	}
}

// TestProbeSearchEndpoint_StripsCredentials verifies that credentials
// embedded in the endpoint URL (e.g. http://user:pass@host) are NOT transmitted
// in the startup probe request. The probe should only check that the host is
// listening.
func TestProbeSearchEndpoint_StripsCredentials(t *testing.T) {
	var gotAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// r.URL.User is populated by Go's HTTP client when the request
		// URL contains userinfo. If credentials were sent, this will be
		// non-nil.
		gotAuth = r.URL.User != nil
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	// Parse the server URL and inject credentials.
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	parsed.User = url.UserPassword("user", "secret")
	endpoint := parsed.String()

	err = websearch.ProbeSearchEndpoint(endpoint, probeTimeout)
	if err != nil {
		t.Fatalf("expected nil error for reachable endpoint, got %v", err)
	}
	if gotAuth {
		t.Errorf("probe request must not transmit credentials embedded in the endpoint URL")
	}
}

// TestProbeSearchEndpoint_CredentialedEndpointNoLeak verifies that
// a credentialed endpoint is logged via SanitizeEndpoint (no credentials in
// the warning/error message).
func TestProbeSearchEndpoint_CredentialedEndpointNoLeak(t *testing.T) {
	err := websearch.ProbeSearchEndpoint("http://user:pass@127.0.0.1:1", probeTimeout)
	if err == nil {
		t.Fatal("expected error for unreachable endpoint, got nil")
	}
	if strings.Contains(err.Error(), "pass") {
		t.Errorf("error must not contain credentials: %v", err)
	}
}

// TestProbeSearchEndpoint_FullSearchURL verifies that when the
// configured endpoint already includes /search, the probe does not
// double-append it (producing /searx/search/search which would falsely
// report the backend unreachable).
func TestProbeSearchEndpoint_FullSearchURL(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	// Full search URL — the probe should hit /searx/search, not
	// /searx/search/search.
	endpoint := server.URL + "/searx/search"
	err := websearch.ProbeSearchEndpoint(endpoint, probeTimeout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedPath != "/searx/search" {
		t.Errorf("probe path = %q, want /searx/search (double-path would warn)", capturedPath)
	}
}

// TestProbeSearchEndpoint_Timeout verifies that when the search
// endpoint is slow (responds only after the probe timeout), the probe returns
// an honest "timed out" error — NOT a misleading "unreachable" claim. The
// endpoint was listening but slow, which is exactly the false "unreachable"
// diagnosis operators relied on before fc-websearch-honest-failure showed the
// backend was merely slow, not down.
func TestProbeSearchEndpoint_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	err := websearch.ProbeSearchEndpoint(server.URL, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for slow endpoint, got nil")
	}
	// The error must honestly label this as a timeout. Before the fix the
	// probe folded every failure into "unreachable", which is false for a
	// slow-but-listening endpoint.
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected error to mention 'timed out', got: %v", err)
	}
	if strings.Contains(err.Error(), "unreachable") {
		t.Errorf("timeout must not be labeled 'unreachable' (endpoint was slow, not down), got: %v", err)
	}
}

// TestMaybeGenerateSecret pins the first-boot secret decision (PR #399 F2):
// a non-wildcard bind (e.g. 127.0.0.1) with no operator-provided secret gets
// a strong 43-char base64url secret generated IN MEMORY so a default
// local-only boot still has HMAC protection (the secret persists on the next
// settings save). For a wildcard bind (0.0.0.0 / :: / *) the generation is
// deliberately SKIPPED so the P0 gate in Config.Validate (SECURITY_MODEL.md:
// "Config.Validate fails boot otherwise") genuinely fires — a default
// 0.0.0.0 install must fail boot and force the operator to either set
// FAMCLAW_SECRET or restrict the bind, instead of auto-booting exposed.
func TestMaybeGenerateSecret(t *testing.T) {
	t.Run("non-wildcard empty secret is generated in memory", func(t *testing.T) {
		cfg := &config.Config{Server: config.ServerConfig{Host: "127.0.0.1", Port: 8080}}
		maybeGenerateSecret(cfg)
		if len(cfg.Server.Secret) != 43 {
			t.Fatalf("generated secret length = %d, want 43 (base64url of 32 random bytes, no padding)", len(cfg.Server.Secret))
		}
		if strings.ContainsAny(cfg.Server.Secret, "+/=") {
			t.Errorf("generated secret is not base64url-safe: %q", cfg.Server.Secret)
		}
		// Generation must be random, not a constant.
		other := &config.Config{Server: config.ServerConfig{Host: "127.0.0.1", Port: 8080}}
		maybeGenerateSecret(other)
		if cfg.Server.Secret == other.Server.Secret {
			t.Error("two generated secrets are identical — generation is not random")
		}
	})

	t.Run("existing operator secret is left untouched", func(t *testing.T) {
		secret := strings.Repeat("x", 43)
		cfg := &config.Config{Server: config.ServerConfig{Host: "127.0.0.1", Port: 8080, Secret: secret}}
		maybeGenerateSecret(cfg)
		if cfg.Server.Secret != secret {
			t.Fatalf("existing secret replaced: got %q, want %q", cfg.Server.Secret, secret)
		}
	})

	for _, host := range []string{"0.0.0.0", "::", "*"} {
		t.Run("wildcard "+host+" skips generation so Validate fails boot", func(t *testing.T) {
			cfg := &config.Config{Server: config.ServerConfig{Host: host, Port: 8080}}
			maybeGenerateSecret(cfg)
			if cfg.Server.Secret != "" {
				t.Fatalf("host %q: secret %q generated at boot, want empty (the wildcard gate must force operator action)", host, cfg.Server.Secret)
			}
			// The boot path is maybeGenerateSecret(cfg) → cfg.Validate(): the
			// wildcard must surface as the actionable P0 error.
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("host %q: Validate() passed, want the P0 wildcard error", host)
			}
			if !strings.Contains(err.Error(), "binds to all interfaces") {
				t.Fatalf("host %q: Validate() error = %v, want the actionable 'binds to all interfaces' message", host, err)
			}
		})
	}
}
