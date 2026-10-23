package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DeviceStatus is the parsed device.status payload (design §4).
type DeviceStatus struct {
	State      string `json:"state"` // ready | booting | offline | unauthorized | down
	Booted     bool   `json:"booted"`
	ScreenOn   bool   `json:"screen_on"`
	Locked     bool   `json:"locked"`
	Foreground string `json:"foreground,omitempty"`
}

// uiDumpPath is the on-device scratch file for the uiautomator dump.
const uiDumpPath = "/sdcard/ui.xml"

// pngMagic is the 8-byte PNG signature used to validate screencap output.
var pngMagic = []byte{0x89, 'P', 'N', 'G'}

// DeviceStatus queries device state, boot, screen power, lockscreen and the
// foreground package (design §4 device.status row). A missing device is
// returned as state "down" with no error; hard adb failures are errors.
func (e *ADBExecutor) DeviceStatus(ctx context.Context) (DeviceStatus, error) {
	st := DeviceStatus{}
	serial, err := e.resolveSerial(ctx)
	if err != nil {
		if errors.Is(err, ErrNoDevice) {
			st.State = "down"
			return st, nil
		}
		return st, err
	}
	out, err := e.run(ctx, serial, "get-state")
	if err != nil {
		return st, fmt.Errorf("get-state: %w", err)
	}
	switch strings.TrimSpace(string(out)) {
	case "device":
		st.State = "booting"
	case "offline":
		st.State = "offline"
		return st, nil
	case "unauthorized":
		st.State = "unauthorized"
		return st, nil
	default:
		st.State = "down"
		return st, nil
	}
	out, err = e.run(ctx, serial, "shell", "getprop", "sys.boot_completed")
	if err != nil {
		return st, fmt.Errorf("boot state: %w", err)
	}
	if strings.TrimSpace(string(out)) != "1" {
		return st, nil // still booting
	}
	out, err = e.run(ctx, serial, "shell", "dumpsys", "power")
	if err != nil {
		return st, fmt.Errorf("power state: %w", err)
	}
	st.ScreenOn = strings.Contains(string(out), "Display Power: state=ON")
	out, err = e.run(ctx, serial, "shell", "dumpsys", "window")
	if err != nil {
		return st, fmt.Errorf("window state: %w", err)
	}
	st.Locked = strings.Contains(string(out), "mDreamingLockscreen=true")
	out, err = e.run(ctx, serial, "shell", "dumpsys", "activity", "activities")
	if err == nil {
		st.Foreground = foregroundPackage(string(out))
	}
	st.Booted = true
	st.State = "ready"
	return st, nil
}

// Screenshot returns the device screen as PNG bytes (design §3.3).
func (e *ADBExecutor) Screenshot(ctx context.Context) ([]byte, error) {
	serial, err := e.resolveSerial(ctx)
	if err != nil {
		return nil, err
	}
	out, err := e.run(ctx, serial, "exec-out", "screencap", "-p")
	if err != nil {
		return nil, fmt.Errorf("screencap: %w", err)
	}
	if !bytes.HasPrefix(out, pngMagic) {
		return nil, fmt.Errorf("screencap returned %d bytes of non-PNG output", len(out))
	}
	return out, nil
}

// UIDump returns the uiautomator hierarchy XML, cached for dumpTTL
// (design §4: dump only when needed).
func (e *ADBExecutor) UIDump(ctx context.Context) (string, error) {
	e.dumpMu.Lock()
	defer e.dumpMu.Unlock()
	if e.dumpXML != "" && time.Since(e.dumpAt) < e.dumpTTL {
		return e.dumpXML, nil
	}
	serial, err := e.resolveSerial(ctx)
	if err != nil {
		return "", err
	}
	if _, err := e.run(ctx, serial, "shell", "uiautomator", "dump", uiDumpPath); err != nil {
		return "", fmt.Errorf("uiautomator dump: %w", err)
	}
	out, err := e.run(ctx, serial, "exec-out", "cat", uiDumpPath)
	if err != nil {
		return "", fmt.Errorf("reading ui dump: %w", err)
	}
	if _, err := e.run(ctx, serial, "shell", "rm", "-f", uiDumpPath); err != nil {
		// Best-effort on-device cleanup; a stale file is harmless.
	}
	e.dumpXML = string(out)
	e.dumpAt = time.Now()
	return e.dumpXML, nil
}

// InputTap taps at device pixel coordinates (design §5.1: no scale
// conversion — the LLM uses pixel coords from find/screenshot).
func (e *ADBExecutor) InputTap(ctx context.Context, x, y int) error {
	serial, err := e.resolveSerial(ctx)
	if err != nil {
		return err
	}
	_, err = e.run(ctx, serial, "shell", "input", "tap",
		strconv.Itoa(x), strconv.Itoa(y))
	return errwrap("tap", err)
}

// InputText types text one character at a time so spaces and shell-special
// characters survive the device shell (design §1).
func (e *ADBExecutor) InputText(ctx context.Context, text string) error {
	serial, err := e.resolveSerial(ctx)
	if err != nil {
		return err
	}
	runes := []rune(text)
	for i, r := range runes {
		if _, err := e.run(ctx, serial, "shell", "input", "text", escapeInputRune(r)); err != nil {
			return errwrap(fmt.Sprintf("typing char %d/%d", i+1, len(runes)), err)
		}
	}
	return nil
}

// keyEventCodes maps key names to Android keycodes.
var keyEventCodes = map[string]int{
	"home": 3, "back": 4, "recents": 187, "menu": 82,
}

// KeyEvent sends a named Android key event.
func (e *ADBExecutor) KeyEvent(ctx context.Context, key string) error {
	code, ok := keyEventCodes[strings.ToLower(key)]
	if !ok {
		return fmt.Errorf("unknown key %q (want home, back, recents, menu)", key)
	}
	serial, err := e.resolveSerial(ctx)
	if err != nil {
		return err
	}
	_, err = e.run(ctx, serial, "shell", "input", "keyevent", strconv.Itoa(code))
	return errwrap("keyevent", err)
}

// Swipe performs an input swipe; ms<=0 defaults to 300ms.
func (e *ADBExecutor) Swipe(ctx context.Context, x1, y1, x2, y2, ms int) error {
	if ms <= 0 {
		ms = 300
	}
	serial, err := e.resolveSerial(ctx)
	if err != nil {
		return err
	}
	_, err = e.run(ctx, serial, "shell", "input", "swipe",
		strconv.Itoa(x1), strconv.Itoa(y1),
		strconv.Itoa(x2), strconv.Itoa(y2), strconv.Itoa(ms))
	return errwrap("swipe", err)
}

// StartApp launches pkg/Activity via `am start -W` and returns the raw
// output (stage 3 validates the component against the allowlist first).
func (e *ADBExecutor) StartApp(ctx context.Context, component string) (string, error) {
	serial, err := e.resolveSerial(ctx)
	if err != nil {
		return "", err
	}
	out, err := e.run(ctx, serial, "shell", "am", "start", "-W", "-n", component)
	if err != nil {
		return "", fmt.Errorf("am start: %w", err)
	}
	return string(out), nil
}

// Shell runs an arbitrary command on the device. v1: operator-only, gated
// behind policy.Tools.Shell; never exposed to the LLM (design §4).
func (e *ADBExecutor) Shell(ctx context.Context, command string) (string, error) {
	serial, err := e.resolveSerial(ctx)
	if err != nil {
		return "", err
	}
	out, err := e.run(ctx, serial, "shell", command)
	if err != nil {
		return "", fmt.Errorf("device shell: %w", err)
	}
	return string(out), nil
}

// escapeInputRune quotes one character for `adb shell input text`.
func escapeInputRune(r rune) string {
	switch r {
	case ' ', '\'', '"', '(', ')', '&', '|', ';', '<', '>', '$',
		'`', '\\', '!', '#', '*':
		return "'" + string(r) + "'"
	default:
		return string(r)
	}
}

// foregroundPackage extracts the foreground app package from
// `dumpsys activity activities` (the mResumedActivity line).
func foregroundPackage(out string) string {
	i := strings.Index(out, "mResumedActivity")
	if i < 0 {
		return ""
	}
	for _, tok := range strings.Fields(out[i:]) {
		if strings.Contains(tok, "/") {
			if pkg := strings.SplitN(tok, "/", 2)[0]; strings.Contains(pkg, ".") {
				return pkg
			}
		}
	}
	return ""
}

// errwrap wraps a non-nil error with an action label; nil passes through.
func errwrap(label string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", label, err)
}
