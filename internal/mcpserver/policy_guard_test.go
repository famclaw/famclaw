package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// guardCfg returns the base policy config for Guard unit tests: one
// full-mode app, one deny-listed app, screen-off refusal, all tools on.
// Cases override individual fields.
func guardCfg() *PolicyConfig {
	return &PolicyConfig{
		DeviceSerial: "emulator-5554",
		DeviceAlias:  "test-device",
		Apps:         []AppEntry{{Package: "com.example.app", Mode: ModeFull}},
		Deny:         []string{"com.example.vault"},
		Tools:        map[string]bool{},
	}
}

func TestGuard(t *testing.T) {
	cases := []struct {
		name       string
		tool       string
		args       map[string]any
		modify     func(*PolicyConfig) // tweak config before building the engine
		setup      func(*fakeADB)      // register/override adb fixtures
		wantErr    string              // substring of the refusal; "" = allowed
		noADBCalls bool                // assert zero adb calls (kill switch must short-circuit)
	}{
		// Kill switches (design §5.2): refused before any probe.
		{
			name:       "shell disabled by kill switch",
			tool:       "shell",
			args:       map[string]any{"cmd": "uptime"},
			modify:     func(c *PolicyConfig) { c.Tools["shell"] = false },
			wantErr:    "disabled by policy",
			noADBCalls: true,
		},
		{
			name:       "tap disabled by kill switch",
			tool:       "tap",
			args:       map[string]any{"x": 100, "y": 200},
			modify:     func(c *PolicyConfig) { c.Tools["tap"] = false },
			wantErr:    "disabled by policy",
			noADBCalls: true,
		},
		{
			name:       "screenshot disabled by kill switch",
			tool:       "screenshot",
			args:       nil,
			modify:     func(c *PolicyConfig) { c.Tools["screenshot"] = false },
			wantErr:    "disabled by policy",
			noADBCalls: true,
		},

		// Screen-on gate (design §5.2): mutating tools probe first.
		{
			name: "tap refused when screen is off",
			tool: "tap",
			args: map[string]any{"x": 100, "y": 200},
			setup: func(f *fakeADB) {
				f.Set("-s emulator-5554 shell dumpsys power", "Display Power: state=OFF")
			},
			wantErr: "screen is off",
		},
		{
			name: "type refused when screen is off",
			tool: "type",
			args: map[string]any{"text": "hello"},
			setup: func(f *fakeADB) {
				f.Set("-s emulator-5554 shell dumpsys power", "Display Power: state=OFF")
			},
			wantErr: "screen is off",
		},
		{
			name: "read-only device_status allowed when screen is off",
			tool: "device_status",
			args: nil,
			setup: func(f *fakeADB) {
				f.Set("-s emulator-5554 shell dumpsys power", "Display Power: state=OFF")
			},
			wantErr: "",
		},

		// Foreground-package allowlist (design §5.4): tap/type only.
		{
			name:    "tap allowed when full-mode app is foreground",
			tool:    "tap",
			args:    map[string]any{"x": 100, "y": 200},
			wantErr: "",
		},
		{
			name: "tap refused when unknown (read-only) app is foreground",
			tool: "tap",
			args: map[string]any{"x": 100, "y": 200},
			setup: func(f *fakeADB) {
				f.Set("-s emulator-5554 shell dumpsys activity activities",
					"mResumedActivity=ActivityRecord{abc u0 com.example.unknown/.MainActivity t42}")
			},
			wantErr: "read-only mode",
		},
		{
			name: "tap refused when deny-listed app is foreground",
			tool: "tap",
			args: map[string]any{"x": 100, "y": 200},
			setup: func(f *fakeADB) {
				f.Set("-s emulator-5554 shell dumpsys activity activities",
					"mResumedActivity=ActivityRecord{abc u0 com.example.vault/.MainActivity t42}")
			},
			wantErr: "deny list",
		},
		{
			name: "type refused when no foreground app detected",
			tool: "type",
			args: map[string]any{"text": "hello"},
			setup: func(f *fakeADB) {
				f.Set("-s emulator-5554 shell dumpsys activity activities", "TaskList: no activities")
			},
			wantErr: "no foreground app",
		},
		{
			name:    "keyevent allowed without foreground check",
			tool:    "keyevent",
			args:    map[string]any{"key": 4},
			wantErr: "",
		},
		{
			name:    "swipe allowed without foreground check",
			tool:    "swipe",
			args:    map[string]any{"x1": 0, "y1": 0, "x2": 50, "y2": 100},
			wantErr: "",
		},

		// Credential-content hard stop (design §5.6): type only.
		{
			name:    "type refused for 6-digit OTP",
			tool:    "type",
			args:    map[string]any{"text": "123456"},
			wantErr: "credential/OTP/passcode",
		},
		{
			name:    "type refused when text contains 'password'",
			tool:    "type",
			args:    map[string]any{"text": "my password is x"},
			wantErr: "credential/OTP/passcode",
		},
		{
			name:    "type refused when text contains 'one-time code'",
			tool:    "type",
			args:    map[string]any{"text": "enter your one-time code"},
			wantErr: "credential/OTP/passcode",
		},
		{
			name:    "type allowed for benign text",
			tool:    "type",
			args:    map[string]any{"text": "hello world"},
			wantErr: "",
		},

		// open_app target-mode check (design §5.4).
		{
			name:    "open_app allowed for full-mode package",
			tool:    "open_app",
			args:    map[string]any{"package": "com.example.app"},
			wantErr: "",
		},
		{
			name:    "open_app refused for unknown (read-only) package",
			tool:    "open_app",
			args:    map[string]any{"package": "com.example.unknown"},
			wantErr: "read_only",
		},
		{
			name:    "open_app refused for deny-listed package",
			tool:    "open_app",
			args:    map[string]any{"package": "com.example.vault"},
			wantErr: "deny",
		},

		// Shell destructive-pattern refuse (design §5.5).
		{
			name:    "shell refused for pm clear",
			tool:    "shell",
			args:    map[string]any{"cmd": "pm clear com.example.app"},
			wantErr: "destructive shell",
		},
		{
			name:    "shell refused for pm uninstall",
			tool:    "shell",
			args:    map[string]any{"cmd": "pm uninstall com.example.app"},
			wantErr: "destructive shell",
		},
		{
			name:    "shell refused for reboot",
			tool:    "shell",
			args:    map[string]any{"cmd": "reboot"},
			wantErr: "destructive shell",
		},
		{
			name:    "shell refused for format /data",
			tool:    "shell",
			args:    map[string]any{"cmd": "format /data"},
			wantErr: "destructive shell",
		},
		{
			name:    "shell refused for REBOOT (case-insensitive)",
			tool:    "shell",
			args:    map[string]any{"cmd": "REBOOT now"},
			wantErr: "destructive shell",
		},
		{
			name:    "shell allowed for benign command",
			tool:    "shell",
			args:    map[string]any{"cmd": "uptime"},
			wantErr: "",
		},
		{
			name:    "shell allowed for pm list packages",
			tool:    "shell",
			args:    map[string]any{"cmd": "pm list packages"},
			wantErr: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeADB()
			guardFixtures(fake)
			if tc.setup != nil {
				tc.setup(fake)
			}
			cfg := guardCfg()
			// shell defaults to off (explicit opt-in, design §5.5); cases
			// that exercise shell semantics opt in unless they test the
			// kill switch itself.
			if strings.HasPrefix(tc.tool, "shell") && tc.name != "shell disabled by kill switch" {
				cfg.Tools["shell"] = true
			}
			if tc.modify != nil {
				tc.modify(cfg)
			}
			pe := NewPolicyEngine(cfg, newTestExecutor(t, fake))

			err := pe.Guard(context.Background(), tc.tool, tc.args)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Guard(%s): expected allow, got: %v", tc.tool, err)
				}
			} else {
				if err == nil {
					t.Fatalf("Guard(%s): expected refusal containing %q, got nil", tc.tool, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Guard(%s): error %q does not contain %q", tc.tool, err.Error(), tc.wantErr)
				}
			}
			if tc.noADBCalls && len(fake.calls()) != 0 {
				t.Errorf("kill switch must refuse before any adb call, got %#v", fake.calls())
			}
		})
	}
}

// TestRateLimiter pins the per-tool per-second caps (design §5.5):
// tap 5/s, type 30/s, screenshot 2/s; everything else unlimited.
func TestRateLimiter(t *testing.T) {
	cases := []struct {
		tool     string
		requests int
		allowed  int
	}{
		{"tap", 5, 5},
		{"tap", 6, 5},
		{"type", 30, 30},
		{"type", 31, 30},
		{"screenshot", 2, 2},
		{"screenshot", 3, 2},
		{"keyevent", 100, 100},
		{"open_app", 100, 100},
		{"frobnicate", 100, 100},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s_x%d", tc.tool, tc.requests), func(t *testing.T) {
			rl := newRateLimiter()
			allowed := 0
			var lastErr error
			for i := 0; i < tc.requests; i++ {
				err := rl.allow(tc.tool)
				if err != nil {
					lastErr = err
					break
				}
				allowed++
			}
			if allowed != tc.allowed {
				t.Fatalf("tool %q: allowed %d of %d, want %d", tc.tool, allowed, tc.requests, tc.allowed)
			}
			if tc.allowed < tc.requests {
				if lastErr == nil || !strings.Contains(lastErr.Error(), "rate limit") {
					t.Fatalf("expected rate-limit error, got: %v", lastErr)
				}
			}
		})
	}
}

// TestRateLimiter_WindowExpiry verifies old calls fall out of the
// sliding 1-second window.
func TestRateLimiter_WindowExpiry(t *testing.T) {
	rl := newRateLimiter()
	old := time.Now().Add(-2 * time.Second)
	for i := 0; i < 5; i++ {
		rl.calls["tap"] = append(rl.calls["tap"], old)
	}
	if err := rl.allow("tap"); err != nil {
		t.Fatalf("calls outside the window must not count: %v", err)
	}
}

// TestRateLimiter_Concurrent caps concurrent callers at the per-tool limit.
func TestRateLimiter_Concurrent(t *testing.T) {
	rl := newRateLimiter()
	const workers = 20
	var mu sync.Mutex
	allowed := 0
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rl.allow("tap") == nil {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 5 {
		t.Fatalf("concurrent: allowed %d, want 5 (tap cap)", allowed)
	}
}

// TestRateLimiter_Reset clears state.
func TestRateLimiter_Reset(t *testing.T) {
	rl := newRateLimiter()
	for i := 0; i < 5; i++ {
		_ = rl.allow("tap")
	}
	if err := rl.allow("tap"); err == nil {
		t.Fatal("expected rate-limit error after 5 tap calls")
	}
	rl.reset()
	if err := rl.allow("tap"); err != nil {
		t.Fatalf("reset must clear the window: %v", err)
	}
}

// TestSanitizeResult verifies the no-leak gate (design §5.7): the configured
// adb serial never survives in tool results.
func TestSanitizeResult(t *testing.T) {
	cases := []struct {
		name   string
		serial string
		texts  []string
		want   []string
	}{
		{
			name:   "serial in text is redacted",
			serial: "emulator-5554",
			texts:  []string{"device emulator-5554 responded"},
			want:   []string{"device [SERIAL] responded"},
		},
		{
			name:   "multiple occurrences redacted",
			serial: "emulator-5554",
			texts:  []string{"emulator-5554 and emulator-5554 again"},
			want:   []string{"[SERIAL] and [SERIAL] again"},
		},
		{
			name:   "clean text untouched",
			serial: "emulator-5554",
			texts:  []string{"all good"},
			want:   []string{"all good"},
		},
		{
			name:   "empty serial is a no-op",
			serial: "",
			texts:  []string{"raw emulator-5554"},
			want:   []string{"raw emulator-5554"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pe := NewPolicyEngine(&PolicyConfig{DeviceSerial: tc.serial}, nil)
			content := make([]mcp.Content, len(tc.texts))
			for i, s := range tc.texts {
				content[i] = mcp.NewTextContent(s)
			}
			res := &mcp.CallToolResult{Content: content}
			out := pe.SanitizeResult(res)
			for i, c := range out.Content {
				got, ok := c.(mcp.TextContent)
				if !ok {
					t.Fatalf("content %d: not TextContent, got %T", i, c)
				}
				if got.Text != tc.want[i] {
					t.Errorf("content %d: got %q, want %q", i, got.Text, tc.want[i])
				}
			}
			// Original must NOT be mutated: input content keeps its original text.
			for i, c := range res.Content {
				if c.(mcp.TextContent).Text != tc.texts[i] {
					t.Errorf("SanitizeResult mutated input content %d: %q, want %q", i, c.(mcp.TextContent).Text, tc.texts[i])
				}
			}
		})
	}
}

// TestSanitizeError verifies error-text redaction on the dispatch path.
func TestSanitizeError(t *testing.T) {
	pe := NewPolicyEngine(&PolicyConfig{DeviceSerial: "emulator-5554"}, nil)
	got := pe.sanitizeError(errors.New("adb: no device emulator-5554 found"))
	if want := "adb: no device [SERIAL] found"; got.Error() != want {
		t.Fatalf("sanitizeError: got %q, want %q", got.Error(), want)
	}
	if err := pe.sanitizeError(nil); err != nil {
		t.Fatalf("sanitizeError(nil): got %v, want nil", err)
	}
	// No serial configured: pass through untouched.
	pe2 := NewPolicyEngine(&PolicyConfig{}, nil)
	if err := pe2.sanitizeError(errors.New("raw emulator-5554")); err.Error() != "raw emulator-5554" {
		t.Fatalf("no-serial pass-through: got %q", err.Error())
	}
}
