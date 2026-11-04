package admin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
)

// writeConfigFile writes a minimal but valid config file for the
// SharedConfigManager tests.
func writeConfigFile(t *testing.T, path string) {
	t.Helper()
	body := "server:\n  host: localhost\n  port: 8080\n  secret: test-secret\nusers:\n  - name: parent\n    role: parent\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config file: %v", err)
	}
}

// TestCurrentToken_Sentinels pins review S4: CurrentToken is fail-closed —
// a missing file is the vacuous precondition ("", nil), but any other
// read failure returns the ErrTokenReadFailed sentinel so handlers abort
// instead of saving against a blind overwrite.
func TestCurrentToken_Sentinels(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.yaml")
	writeConfigFile(t, existing)

	cases := []struct {
		name      string
		path      string
		wantEmpty bool // want an empty (vacuous) token
	}{
		{"empty path is vacuous", "", true},
		{"missing file is vacuous", filepath.Join(dir, "absent.yaml"), true},
		{"existing file returns its sha256", existing, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scm := NewSharedConfigManager(tc.path)
			got, err := scm.CurrentToken()
			if err != nil {
				t.Fatalf("CurrentToken() err = %v, want nil", err)
			}
			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("CurrentToken() = %q, want vacuous \"\"", got)
				}
			} else {
				if got == "" {
					t.Fatalf("CurrentToken() = empty for an existing file, want its content token")
				}
				// Determinism: two managers over the same path agree.
				other := NewSharedConfigManager(tc.path)
				got2, err := other.CurrentToken()
				if err != nil || got2 != got {
					t.Fatalf("CurrentToken() not deterministic across managers: %q/%v vs %q", got, err, got2)
				}
			}
		})
	}

	// Read failure sentinel: a directory at the config path is present
	// but unreadable as a file.
	dirPath := filepath.Join(dir, "a-dir")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatalf("making dir: %v", err)
	}
	scm := NewSharedConfigManager(dirPath)
	if _, err := scm.CurrentToken(); !errors.Is(err, ErrTokenReadFailed) {
		t.Fatalf("CurrentToken() on unreadable path: err = %v, want ErrTokenReadFailed", err)
	}
}

// TestSaveConfig_OperatorConflict pins review S3: when the on-disk content
// changes between the precondition read and the commit, SaveConfig returns
// the ErrOperatorConflict sentinel (detectable via errors.Is — no more
// strings.Contains) so handlers can roll back / never publish.
func TestSaveConfig_OperatorConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeConfigFile(t, path)

	scm := NewSharedConfigManager(path)
	token, err := scm.CurrentToken()
	if err != nil {
		t.Fatalf("CurrentToken(): %v", err)
	}

	// Simulate an external process editing the file after our read.
	if err := os.WriteFile(path, []byte("server:\n  host: other\n  port: 9999\n  secret: external-edit\n"), 0o600); err != nil {
		t.Fatalf("external edit: %v", err)
	}

	cfg := &config.Config{Server: config.ServerConfig{Host: "localhost", Port: 8080, Secret: "test-secret"}}
	_, err = scm.SaveConfig(cfg, token)
	if !errors.Is(err, ErrOperatorConflict) {
		t.Fatalf("SaveConfig() after external edit: err = %v, want ErrOperatorConflict", err)
	}
}

// TestSaveConfig_CommitAndTokenRefresh pins the happy path: a confirmed
// commit updates the on-disk bytes and the token moves to the new content
// (a second save with the stale token must then conflict).
func TestSaveConfig_CommitAndTokenRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeConfigFile(t, path)

	scm := NewSharedConfigManager(path)
	token, err := scm.CurrentToken()
	if err != nil {
		t.Fatalf("CurrentToken(): %v", err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{Host: "localhost", Port: 8080, Secret: "test-secret"},
		Tools:  config.ToolsConfig{WebFetch: config.WebFetchConfig{Enabled: true, URLDenylist: []string{"example.com"}}},
	}
	newToken, err := scm.SaveConfig(cfg, token)
	if err != nil {
		t.Fatalf("SaveConfig() happy path: %v", err)
	}
	if newToken == "" {
		t.Fatalf("SaveConfig() returned an empty new token")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading committed file: %v", err)
	}
	if !bytes.Contains(data, []byte("example.com")) {
		t.Fatalf("committed file does not contain the new denylist entry:\n%s", data)
	}
	// The committed token matches the new file content.
	got, err := scm.CurrentToken()
	if err != nil {
		t.Fatalf("CurrentToken() after commit: %v", err)
	}
	if got != newToken {
		t.Fatalf("token after commit = %q, want the returned new token %q", got, newToken)
	}
	// A save with the STALE token now conflicts (external-edit semantics).
	if _, err := scm.SaveConfig(cfg, token); !errors.Is(err, ErrOperatorConflict) {
		t.Fatalf("SaveConfig() with stale token: err = %v, want ErrOperatorConflict", err)
	}
}

// TestNotifyChange_UsesWriteLock pins review M1: NotifyChange assigns
// cw.eventToken, so it must take the write lock — concurrent NotifyChange
// calls must not race. Run with -race.
func TestNotifyChange_UsesWriteLock(t *testing.T) {
	cw := NewConfigWatcher(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cw.NotifyChange(fmt.Sprintf("token-%d", i))
		}(i)
	}
	wg.Wait()
	// eventToken holds exactly one of the tokens (last writer wins),
	// and SelfEventSuppression reads it without racing.
	last := cw.eventToken
	if last == "" {
		t.Fatalf("eventToken empty after concurrent NotifyChange")
	}
	if !cw.SelfEventSuppression(last) {
		t.Fatalf("SelfEventSuppression(%q) = false, want true", last)
	}
}

// TestHandleDenylistAdd_TokenReadFailureFailsClosed pins review S4 at the
// handler level: when the on-disk state cannot be read (token precondition
// fails), denylist_add must abort BEFORE mutating the in-memory list and
// surface the ErrTokenReadFailed sentinel — no blind overwrite, no
// partial publish.
func TestHandleDenylistAdd_TokenReadFailureFailsClosed(t *testing.T) {
	// A directory at the config path is present but unreadable as a file.
	dir := t.TempDir()
	cfg := &config.Config{Users: []config.UserConfig{{Name: "parent", Role: "parent"}}}
	deps := Deps{
		DB:                nil,
		Cfg:               cfg,
		Actor:             "parent",
		Gateway:           "test",
		ConfigPath:        dir,
		SharedConfig:      NewSharedConfigManager(dir),
		SharedPolicyOwner: cfg.WebFetchPolicyOwner(),
	}
	before := cfg.Tools.WebFetch.URLDenylist

	out, err := HandleDenylistAdd(context.Background(), deps, map[string]any{"host": "example.com"})
	if !errors.Is(err, ErrTokenReadFailed) {
		t.Fatalf("HandleDenylistAdd with unreadable token source: err = %v (out=%q), want ErrTokenReadFailed", err, out)
	}
	if after := cfg.Tools.WebFetch.URLDenylist; !reflect.DeepEqual(after, before) {
		t.Fatalf("denylist mutated on fail-closed abort: %v -> %v", before, after)
	}
}
