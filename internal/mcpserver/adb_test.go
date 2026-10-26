package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newTestExecutor(t *testing.T, fake *fakeADB, opts ...func(*ADBExecutor)) *ADBExecutor {
	t.Helper()
	e := NewADBExecutor("family-pi", fake, 2*time.Second, 200*time.Millisecond, "")
	for _, o := range opts {
		o(e)
	}
	return e
}

func withPin(pin string) func(*ADBExecutor) {
	return func(e *ADBExecutor) { e.pin = pin }
}

func TestResolveSerial(t *testing.T) {
	tests := []struct {
		name       string
		online     []string
		pin        string
		wantSerial string
		wantErr    string // substring; empty = no error
	}{
		{name: "single online device", online: []string{"emulator-5554"}, wantSerial: "emulator-5554"},
		{name: "no devices", wantErr: "no online device"},
		{name: "multiple without pin", online: []string{"a", "b"}, wantErr: "2 devices online"},
		{name: "pin selects", online: []string{"a", "b"}, pin: "b", wantSerial: "b"},
		{name: "pin not online", online: []string{"a"}, pin: "zz", wantErr: "not online"},
		{name: "offline entries ignored", online: []string{}, wantErr: "no online device"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeADB()
			fake.setDevices(tt.online...)
			e := newTestExecutor(t, fake, withPin(tt.pin))
			got, err := e.resolveSerial(context.Background())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want err %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tt.wantSerial {
				t.Fatalf("want serial %q, got %q", tt.wantSerial, got)
			}
		})
	}
}

func TestRedactSerial(t *testing.T) {
	tests := []struct {
		serial string
		msg    string
		want   string
	}{
		{"", "some error", "some error"},
		{"emulator-5554", "cannot connect to emulator-5554:5555", "cannot connect to device:5555"},
		{"emulator-5554", "serial emulator-5554 emulator-5554 twice", "serial device device twice"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := redact(tt.serial, tt.msg); got != tt.want {
				t.Fatalf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestRun_Timeout(t *testing.T) {
	// A runner that always blocks and never observes the deadline: the
	// executor's own context.WithTimeout must produce a controlled message.
	blocking := &blockingRunner{}
	e := NewADBExecutor("family-pi", blocking, 50*time.Millisecond, 0, "")
	start := time.Now()
	_, err := e.run(context.Background(), "emulator-5554", "shell", "true")
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout error, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("timeout not bounded: %s", time.Since(start))
	}
	if strings.Contains(err.Error(), "emulator-5554") {
		t.Fatalf("timeout error leaks serial: %v", err)
	}
}

// blockingRunner blocks until ctx is done, then reports the cancellation.
type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, argv ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestOnlineDevices(t *testing.T) {
	out := "List of devices attached\n\nemulator-5554	device\n192.168.1.50:5555	device\noffline1	offline\nunauth	unauthorized\n"
	got := onlineDevices(out)
	want := []string{"emulator-5554", "192.168.1.50:5555"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

func TestDeviceStatus(t *testing.T) {
	tests := []struct {
		name    string
		state   string
		booted  string
		power   string
		window  string
		acts    string
		want    DeviceStatus
		wantErr bool
	}{
		{
			name:   "ready unlocked",
			state:  "device",
			booted: "1",
			power:  "Display Power: state=ON",
			window: "mDreamingLockscreen=false",
			acts:   "mResumedActivity=ActivityRecord{abc u0 com.google.android.apps.maps/.MapsActivity}",
			want:   DeviceStatus{State: "ready", Booted: true, ScreenOn: true, Locked: false, Foreground: "com.google.android.apps.maps"},
		},
		{
			name:   "locked",
			state:  "device",
			booted: "1",
			power:  "Display Power: state=ON",
			window: "mDreamingLockscreen=true",
			acts:   "mResumedActivity=ActivityRecord{def u0 com.android.systemui/.SystemUIService}",
			want:   DeviceStatus{State: "ready", Booted: true, ScreenOn: true, Locked: true, Foreground: "com.android.systemui"},
		},
		{
			name:   "screen off",
			state:  "device",
			booted: "1",
			power:  "Display Power: state=OFF",
			window: "mDreamingLockscreen=false",
			want:   DeviceStatus{State: "ready", Booted: true, ScreenOn: false, Locked: false},
		},
		{
			name:   "booting",
			state:  "device",
			booted: "0",
			want:   DeviceStatus{State: "booting"},
		},
		{name: "offline", state: "offline", want: DeviceStatus{State: "offline"}},
		{name: "unauthorized", state: "unauthorized", want: DeviceStatus{State: "unauthorized"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeADB()
			fake.setDevices("emulator-5554")
			fake.Set("-s emulator-5554 get-state", tt.state)
			if tt.booted != "" {
				fake.Set("-s emulator-5554 shell getprop sys.boot_completed", tt.booted)
			}
			if tt.power != "" {
				fake.Set("-s emulator-5554 shell dumpsys power", tt.power)
			}
			if tt.window != "" {
				fake.Set("-s emulator-5554 shell dumpsys window", tt.window)
			}
			if tt.acts != "" {
				fake.Set("-s emulator-5554 shell dumpsys activity activities", tt.acts)
			}
			e := newTestExecutor(t, fake)
			got, err := e.DeviceStatus(context.Background())
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tt.want {
				t.Fatalf("want %+v, got %+v", tt.want, got)
			}
		})
	}
	t.Run("no device is state down, not an error", func(t *testing.T) {
		fake := newFakeADB()
		fake.setDevices() // zero online
		e := newTestExecutor(t, fake)
		got, err := e.DeviceStatus(context.Background())
		if err != nil {
			t.Fatalf("want nil error, got %v", err)
		}
		if got.State != "down" {
			t.Fatalf("want state down, got %q", got.State)
		}
	})
}

func TestScreenshot(t *testing.T) {
	t.Run("png passes through", func(t *testing.T) {
		png := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte("fake-png-body")...)
		fake := newFakeADB()
		fake.setDevices("emulator-5554")
		fake.Set("-s emulator-5554 exec-out screencap -p", string(png))
		e := newTestExecutor(t, fake)
		got, err := e.Screenshot(context.Background())
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if string(got) != string(png) {
			t.Fatalf("png bytes altered")
		}
	})
	t.Run("non-png output is rejected", func(t *testing.T) {
		fake := newFakeADB()
		fake.setDevices("emulator-5554")
		fake.Set("-s emulator-5554 exec-out screencap -p", "not a png")
		e := newTestExecutor(t, fake)
		if _, err := e.Screenshot(context.Background()); err == nil {
			t.Fatal("want error for non-PNG output")
		}
	})
	t.Run("no device", func(t *testing.T) {
		fake := newFakeADB()
		fake.setDevices()
		e := newTestExecutor(t, fake)
		_, err := e.Screenshot(context.Background())
		if !errors.Is(err, ErrNoDevice) {
			t.Fatalf("want ErrNoDevice, got %v", err)
		}
	})
}

func TestUIDump_Cached(t *testing.T) {
	fake := newFakeADB()
	fake.setDevices("emulator-5554")
	fake.Set("-s emulator-5554 shell uiautomator dump /sdcard/ui.xml", "")
	fake.Set("-s emulator-5554 exec-out cat /sdcard/ui.xml", "<hierarchy></hierarchy>")
	e := newTestExecutor(t, fake)
	ctx := context.Background()

	if _, err := e.UIDump(ctx); err != nil {
		t.Fatalf("first dump: %v", err)
	}
	if _, err := e.UIDump(ctx); err != nil {
		t.Fatalf("cached dump: %v", err)
	}
	if n := fake.countOf("-s emulator-5554 shell uiautomator dump"); n != 1 {
		t.Fatalf("want 1 uiautomator dump call, got %d (cache not working)", n)
	}

	e.InvalidateDump()
	if _, err := e.UIDump(ctx); err != nil {
		t.Fatalf("post-invalidate dump: %v", err)
	}
	if n := fake.countOf("-s emulator-5554 shell uiautomator dump"); n != 2 {
		t.Fatalf("want 2 uiautomator dump calls after invalidate, got %d", n)
	}
}

func TestInputText_EscapesCharacters(t *testing.T) {
	fake := newFakeADB()
	fake.setDevices("emulator-5554")
	// escapeInputRune only quotes shell-special chars; plain letters pass
	// through unquoted, so the argv tail is `... input text <char>`.
	for _, tail := range []string{
		"-s emulator-5554 shell input text H",
		"-s emulator-5554 shell input text e",
		"-s emulator-5554 shell input text l",
		"-s emulator-5554 shell input text o",
		"-s emulator-5554 shell input text ' '",
		"-s emulator-5554 shell input text a",
		"-s emulator-5554 shell input text b",
	} {
		fake.Set(tail, "")
	}
	e := newTestExecutor(t, fake)
	if err := e.InputText(context.Background(), "Hello ab"); err != nil {
		t.Fatalf("InputText: %v", err)
	}
	calls := fake.calls()
	if len(calls) != 9 { // 1 devices + 8 chars
		t.Fatalf("want 9 adb calls, got %d: %v", len(calls), calls)
	}
	// Only the space char is shell-quoted; verify exactly one call carries
	// the quoted-space tail and the letters are unquoted.
	quoted := 0
	for i := 1; i < len(calls); i++ {
		arg := calls[i][len(calls[i])-1]
		if arg == "' '" {
			quoted++
		} else if strings.ContainsRune(arg, '\'') {
			t.Fatalf("non-special char unexpectedly quoted: %q", arg)
		}
	}
	if quoted != 1 {
		t.Fatalf("want exactly 1 quoted-space call, got %d", quoted)
	}
}

func TestKeyEvent(t *testing.T) {
	fake := newFakeADB()
	fake.setDevices("emulator-5554")
	fake.Set("-s emulator-5554 shell input keyevent 4", "")
	e := newTestExecutor(t, fake)
	if err := e.KeyEvent(context.Background(), "back"); err != nil {
		t.Fatalf("back: %v", err)
	}
	if err := e.KeyEvent(context.Background(), "power"); err == nil ||
		!strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("want unknown-key error, got %v", err)
	}
}

func TestSwipe(t *testing.T) {
	fake := newFakeADB()
	fake.setDevices("emulator-5554")
	fake.Set("-s emulator-5554 shell input swipe 100 800 100 200 300", "")
	e := newTestExecutor(t, fake)
	if err := e.Swipe(context.Background(), 100, 800, 100, 200, 0); err != nil {
		t.Fatalf("swipe: %v", err)
	}
}

func TestStartApp(t *testing.T) {
	fake := newFakeADB()
	fake.setDevices("emulator-5554")
	fake.Set("-s emulator-5554 shell am start -W -n com.google.android.apps.maps/.MapsActivity",
		"Status: ok\nLaunchState: COLD")
	e := newTestExecutor(t, fake)
	out, err := e.StartApp(context.Background(), "com.google.android.apps.maps/.MapsActivity")
	if err != nil {
		t.Fatalf("StartApp: %v", err)
	}
	if !strings.Contains(out, "Status: ok") {
		t.Fatalf("want am output, got %q", out)
	}
}

func TestShell(t *testing.T) {
	fake := newFakeADB()
	fake.setDevices("emulator-5554")
	fake.Set("-s emulator-5554 shell settings get global airplane_mode_on", "0")
	e := newTestExecutor(t, fake)
	out, err := e.Shell(context.Background(), "settings get global airplane_mode_on")
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if out != "0" {
		t.Fatalf("want %q, got %q", "0", out)
	}
}
