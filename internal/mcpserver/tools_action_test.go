package mcpserver

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// callToolAllowError is like callTool but tolerates IsError results, for
// cases that intentionally exercise the error path.
func callToolAllowError(t *testing.T, c *client.Client, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

// callLogged reports whether fake recorded a call whose canonicalised argv
// signature equals tail (the fixture-key form).
func callLogged(f *fakeADB, tail string) bool {
	want := canonical(tail)
	for _, c := range f.calls() {
		if sig(c...) == want {
			return true
		}
	}
	return false
}

// inputTextArgs returns the per-character `input text` arguments in call
// order, for asserting the type tool's per-character loop.
func inputTextArgs(f *fakeADB) []string {
	const prefix = "-s " + testSerial + " shell input text "
	var out []string
	for _, c := range f.calls() {
		if s := sig(c...); strings.HasPrefix(s, prefix) {
			out = append(out, strings.TrimPrefix(s, prefix))
		}
	}
	return out
}

// withTypePrecheck sets the executor's type precheck hook (design §5.1
// credential/OTP/passcode hard-stop seam).
func withTypePrecheck(fn func(text string) error) func(*ADBExecutor) {
	return func(e *ADBExecutor) { e.TypePrecheck = fn }
}

// mutationFixtures registers every fixture a mutating tool call needs to
// clear PolicyEngine.Guard (devices list, screen ON, full-mode app in
// foreground), on top of which the test adds its own action fixtures.
func mutationFixtures(f *fakeADB) {
	guardFixtures(f)
}

// tapDumpFixtures registers the fixtures a selector-based tap needs:
// guard probes plus the uiautomator dump and its retrieval.
func tapDumpFixtures(f *fakeADB) {
	mutationFixtures(f)
	f.Set("-s emulator-5554 shell uiautomator dump /sdcard/ui.xml", "")
	f.Set("-s emulator-5554 exec-out cat /sdcard/ui.xml", findDump)
}

func TestDispatch_Tap(t *testing.T) {
	cases := []struct {
		name      string
		args      map[string]any
		fixtures  func(f *fakeADB)
		wantSub   string // substring that must appear in the result text
		wantTap   string // exact input-tap signature that must be logged
		wantNoTap bool   // assert that no input tap was issued
	}{
		{
			name:     "selector text match",
			args:     map[string]any{"text": "hello"},
			fixtures: tapDumpFixtures,
			wantSub:  "tapped (160, 70)",
			wantTap:  "-s emulator-5554 shell input tap 160 70",
		},
		{
			name:     "selector content-desc match",
			args:     map[string]any{"content_desc": "close"},
			fixtures: tapDumpFixtures,
			wantSub:  "tapped (330, 30)",
			wantTap:  "-s emulator-5554 shell input tap 330 30",
		},
		{
			name:     "selector resource-id match",
			args:     map[string]any{"resource_id": "greet"},
			fixtures: tapDumpFixtures,
			wantSub:  "tapped (160, 70)",
			wantTap:  "-s emulator-5554 shell input tap 160 70",
		},
		{
			name:      "no selector match does not tap",
			args:      map[string]any{"text": "nonexistent"},
			fixtures:  tapDumpFixtures,
			wantSub:   "No element matched",
			wantNoTap: true,
		},
		{
			name: "coordinates fallback",
			args: map[string]any{"x": 500, "y": 600},
			fixtures: func(f *fakeADB) {
				mutationFixtures(f)
				f.Set("-s emulator-5554 shell input tap 500 600", "")
			},
			wantSub: "tapped (500, 600)",
			wantTap: "-s emulator-5554 shell input tap 500 600",
		},
		{
			name:    "no args is a soft error, no tap issued",
			args:    map[string]any{},
			wantSub: "tap requires either a selector",
		},
		{
			name:    "single coordinate is a soft error, no tap issued",
			args:    map[string]any{"x": 500},
			wantSub: "tap requires either a selector",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeADB()
			if tc.fixtures != nil {
				tc.fixtures(fake)
			}
			// Guard probes run even when the handler will soft-error.
			if tc.fixtures == nil {
				mutationFixtures(fake)
			}
			if tc.wantTap != "" {
				fake.Set(tc.wantTap, "")
			}
			c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

			res := callTool(t, c, "tap", tc.args)
			got := resultText(res)
			if !strings.Contains(got, tc.wantSub) {
				t.Fatalf("result missing %q:\n%s", tc.wantSub, got)
			}
			if tc.wantTap != "" && !callLogged(fake, tc.wantTap) {
				t.Errorf("missing tap call %q; calls: %#v", tc.wantTap, fake.calls())
			}
			if tc.wantNoTap && fake.countOf("-s emulator-5554 shell input tap") != 0 {
				t.Errorf("expected no input tap, got: %#v", fake.calls())
			}
		})
	}
}

func TestDispatch_Type(t *testing.T) {
	t.Run("plain text is typed per character", func(t *testing.T) {
		fake := newFakeADB()
		mutationFixtures(fake)
		fake.Set("-s emulator-5554 shell input text h", "")
		fake.Set("-s emulator-5554 shell input text i", "")
		c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

		res := callTool(t, c, "type", map[string]any{"text": "hi"})
		if got := resultText(res); !strings.Contains(got, "typed 2 character(s)") {
			t.Errorf("result: %q", got)
		}
		if got, want := inputTextArgs(fake), []string{"h", "i"}; !reflect.DeepEqual(got, want) {
			t.Errorf("input text args: got %#v want %#v", got, want)
		}
	})

	t.Run("special characters survive the device shell", func(t *testing.T) {
		fake := newFakeADB()
		mutationFixtures(fake)
		fake.Set("-s emulator-5554 shell input text a", "")
		fake.Set("-s emulator-5554 shell input text ' '", "")
		fake.Set("-s emulator-5554 shell input text b", "")
		c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

		res := callTool(t, c, "type", map[string]any{"text": "a b"})
		if got := resultText(res); !strings.Contains(got, "typed 3 character(s)") {
			t.Errorf("result: %q", got)
		}
		if got, want := inputTextArgs(fake), []string{"a", "' '", "b"}; !reflect.DeepEqual(got, want) {
			t.Errorf("input text args: got %#v want %#v", got, want)
		}
	})

	t.Run("executor precheck hook still refuses before any input", func(t *testing.T) {
		fake := newFakeADB()
		mutationFixtures(fake)
		e := newTestExecutor(t, fake, withTypePrecheck(func(text string) error {
			return errors.New("credential-looking input is not allowed")
		}))
		c := inProcessClientWithExecutor(t, initPolicy(false), e)

		res := callTool(t, c, "type", map[string]any{"text": "hunter2"})
		got := resultText(res)
		if !strings.Contains(got, "Refused:") || !strings.Contains(got, "credential-looking input is not allowed") {
			t.Errorf("result: %q", got)
		}
		if fake.countOf("-s emulator-5554 shell input text") != 0 {
			t.Errorf("precheck must refuse before any input text call: %#v", fake.calls())
		}
	})

	t.Run("empty text is a soft error, no input issued", func(t *testing.T) {
		fake := newFakeADB()
		mutationFixtures(fake)
		c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

		res := callTool(t, c, "type", map[string]any{})
		if got := resultText(res); !strings.Contains(got, "type requires a non-empty text argument.") {
			t.Errorf("result: %q", got)
		}
		if fake.countOf("-s emulator-5554 shell input text") != 0 {
			t.Errorf("expected no input text calls: %#v", fake.calls())
		}
	})
}

func TestDispatch_KeyEvent(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		wantSub string
		wantKey string // exact keyevent signature that must be logged
	}{
		{name: "home", args: map[string]any{"key": "home"}, wantSub: "sent keyevent", wantKey: "-s emulator-5554 shell input keyevent 3"},
		{name: "back", args: map[string]any{"key": "back"}, wantSub: "sent keyevent", wantKey: "-s emulator-5554 shell input keyevent 4"},
		{name: "recents", args: map[string]any{"key": "recents"}, wantSub: "sent keyevent", wantKey: "-s emulator-5554 shell input keyevent 187"},
		{name: "menu", args: map[string]any{"key": "menu"}, wantSub: "sent keyevent", wantKey: "-s emulator-5554 shell input keyevent 82"},
		{name: "unknown key is a soft error", args: map[string]any{"key": "volumeup"}, wantSub: `unknown key "volumeup"`},
		{name: "missing key is a soft error", args: map[string]any{}, wantSub: "keyevent requires a key argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeADB()
			mutationFixtures(fake)
			if tc.wantKey != "" {
				fake.Set(tc.wantKey, "")
			}
			c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

			res := callTool(t, c, "keyevent", tc.args)
			got := resultText(res)
			if !strings.Contains(got, tc.wantSub) {
				t.Fatalf("result missing %q:\n%s", tc.wantSub, got)
			}
			if tc.wantKey != "" && !callLogged(fake, tc.wantKey) {
				t.Errorf("missing keyevent call %q; calls: %#v", tc.wantKey, fake.calls())
			}
			if tc.wantKey == "" && fake.countOf("-s emulator-5554 shell input keyevent") != 0 {
				t.Errorf("expected no keyevent call: %#v", fake.calls())
			}
		})
	}
}

func TestDispatch_Swipe(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		wantSub string
		wantSig string // exact swipe signature that must be logged
	}{
		{
			name:    "default duration",
			args:    map[string]any{"x1": 10, "y1": 20, "x2": 100, "y2": 200},
			wantSub: "swiped (10,20)\u2192(100,200) over 300ms",
			wantSig: "-s emulator-5554 shell input swipe 10 20 100 200 300",
		},
		{
			name:    "explicit duration",
			args:    map[string]any{"x1": 10, "y1": 20, "x2": 100, "y2": 200, "ms": 500},
			wantSub: "swiped (10,20)\u2192(100,200) over 500ms",
			wantSig: "-s emulator-5554 shell input swipe 10 20 100 200 500",
		},
		{
			name:    "missing coordinates is a soft error",
			args:    map[string]any{"x1": 10, "y1": 20},
			wantSub: "swipe requires x1, y1, x2, y2.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeADB()
			mutationFixtures(fake)
			if tc.wantSig != "" {
				fake.Set(tc.wantSig, "")
			}
			c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

			res := callTool(t, c, "swipe", tc.args)
			got := resultText(res)
			if !strings.Contains(got, tc.wantSub) {
				t.Fatalf("result missing %q:\n%s", tc.wantSub, got)
			}
			if tc.wantSig != "" && !callLogged(fake, tc.wantSig) {
				t.Errorf("missing swipe call %q; calls: %#v", tc.wantSig, fake.calls())
			}
			if tc.wantSig == "" && fake.countOf("-s emulator-5554 shell input swipe") != 0 {
				t.Errorf("expected no swipe call: %#v", fake.calls())
			}
		})
	}
}

func TestDispatch_OpenApp(t *testing.T) {
	const resolveSig = "-s emulator-5554 shell cmd package resolve-activity --brief -c android.intent.category.LAUNCHER com.example.app"

	t.Run("resolves launcher then launches", func(t *testing.T) {
		fake := newFakeADB()
		mutationFixtures(fake)
		fake.Set(resolveSig, "com.example.app/.MainActivity\n")
		fake.Set("-s emulator-5554 shell am start -W -n com.example.app/.MainActivity", "Status: ok\n")
		c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

		res := callTool(t, c, "open_app", map[string]any{"package": "com.example.app"})
		got := resultText(res)
		for _, want := range []string{"opened com.example.app via com.example.app/.MainActivity", "Status: ok"} {
			if !strings.Contains(got, want) {
				t.Errorf("result missing %q:\n%s", want, got)
			}
		}
		if !callLogged(fake, "-s emulator-5554 shell am start -W -n com.example.app/.MainActivity") {
			t.Errorf("missing am start call; calls: %#v", fake.calls())
		}
	})

	t.Run("no launchable activity is an error", func(t *testing.T) {
		fake := newFakeADB()
		mutationFixtures(fake)
		fake.Set(resolveSig, "\n") // blank output → ErrNoLauncher
		c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

		res := callToolAllowError(t, c, "open_app", map[string]any{"package": "com.example.app"})
		if !res.IsError {
			t.Fatalf("want error result, got success: %s", resultText(res))
		}
		if got := resultText(res); !strings.Contains(got, "no launchable activity") {
			t.Errorf("result: %q", got)
		}
		if fake.countOf("-s emulator-5554 shell am start") != 0 {
			t.Errorf("must not launch when resolution fails: %#v", fake.calls())
		}
	})

	t.Run("read-only app is refused by the guard", func(t *testing.T) {
		fake := newFakeADB()
		mutationFixtures(fake)
		c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

		res := callTool(t, c, "open_app", map[string]any{"package": "com.example.banking"})
		if got := resultText(res); !strings.Contains(got, `"read_only" mode`) {
			t.Errorf("result: %q", got)
		}
		if fake.countOf("-s emulator-5554 shell am start") != 0 {
			t.Errorf("must not launch a read-only app: %#v", fake.calls())
		}
	})

	t.Run("deny-listed app is refused by the guard", func(t *testing.T) {
		fake := newFakeADB()
		mutationFixtures(fake)
		c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

		res := callTool(t, c, "open_app", map[string]any{"package": "com.example.vault"})
		if got := resultText(res); !strings.Contains(got, `"deny" mode`) {
			t.Errorf("result: %q", got)
		}
		if fake.countOf("-s emulator-5554 shell am start") != 0 {
			t.Errorf("must not launch a deny-listed app: %#v", fake.calls())
		}
	})

	t.Run("missing package is refused, no launch", func(t *testing.T) {
		fake := newFakeADB()
		mutationFixtures(fake)
		c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

		res := callTool(t, c, "open_app", map[string]any{})
		if got := resultText(res); !strings.Contains(got, "open_app requires a package argument") {
			t.Errorf("result: %q", got)
		}
		if fake.countOf("-s emulator-5554 shell am start") != 0 {
			t.Errorf("must not launch without a package: %#v", fake.calls())
		}
	})
}

func TestDispatch_Shell(t *testing.T) {
	t.Run("executes the command and returns its output", func(t *testing.T) {
		fake := newFakeADB()
		mutationFixtures(fake)
		fake.Set("-s emulator-5554 shell uptime", " 01:02:03 up 1 day\n")
		c := inProcessClientWithExecutor(t, initPolicy(true), newTestExecutor(t, fake))

		res := callTool(t, c, "shell", map[string]any{"cmd": "uptime"})
		if got := resultText(res); got != " 01:02:03 up 1 day\n" {
			t.Errorf("result: %q", got)
		}
		if !callLogged(fake, "-s emulator-5554 shell uptime") {
			t.Errorf("missing shell call; calls: %#v", fake.calls())
		}
	})

	t.Run("destructive command is refused by the guard", func(t *testing.T) {
		fake := newFakeADB()
		mutationFixtures(fake)
		c := inProcessClientWithExecutor(t, initPolicy(true), newTestExecutor(t, fake))

		res := callTool(t, c, "shell", map[string]any{"cmd": "reboot"})
		if got := resultText(res); !strings.Contains(got, "destructive") {
			t.Errorf("result: %q", got)
		}
		if fake.countOf("-s emulator-5554 shell reboot") != 0 {
			t.Errorf("must not run destructive shell: %#v", fake.calls())
		}
	})

	t.Run("shell disabled by policy: tool not registered", func(t *testing.T) {
		fake := newFakeADB()
		c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

		// When the shell tool is disabled, the MCP server does not register
		// it at all (server.go gates AddTool on policy.Tools["shell"]), so
		// the client gets a JSON-RPC "tool not found" error before any
		// adb call.
		shellReq := mcp.CallToolRequest{}
		shellReq.Params.Name = "shell"
		shellReq.Params.Arguments = map[string]any{"cmd": "uptime"}
		_, callErr := c.CallTool(context.Background(), shellReq)
		if callErr == nil || !strings.Contains(callErr.Error(), "not found") {
			t.Errorf("expected JSON-RPC tool-not-found error, got: %v", callErr)
		}
		if n := len(fake.calls()); n != 0 {
			t.Errorf("must not make any adb call for an unregistered tool, got %d: %#v", n, fake.calls())
		}
	})

	t.Run("missing cmd is a soft error, no shell issued", func(t *testing.T) {
		fake := newFakeADB()
		mutationFixtures(fake)
		c := inProcessClientWithExecutor(t, initPolicy(true), newTestExecutor(t, fake))

		res := callTool(t, c, "shell", map[string]any{})
		if got := resultText(res); !strings.Contains(got, "shell requires a cmd argument.") {
			t.Errorf("result: %q", got)
		}
		// Guard's own dumpsys probes are allowed; a user shell command is not.
		for _, c := range fake.calls() {
			s := sig(c...)
			if strings.HasPrefix(s, "-s emulator-5554 shell ") &&
				!strings.HasSuffix(s, "dumpsys power") &&
				!strings.HasSuffix(s, "dumpsys activity activities") {
				t.Errorf("user shell command issued despite missing cmd: %#v", c)
			}
		}
	})
}
