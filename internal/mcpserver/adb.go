package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultADBTimeout bounds a single adb invocation. screencap over USB can
// take several seconds; 30s is generous for that and tight enough to surface
// a wedged device (design §4 executor card).
const DefaultADBTimeout = 30 * time.Second

// DefaultDumpTTL is how long a uiautomator dump stays fresh before find
// re-dumps (design §4: dumps cost ~1-2s; cache between read calls).
const DefaultDumpTTL = 15 * time.Second

// ErrNoDevice is returned by resolveSerial when no online device exists.
// device.status maps it to state "down" instead of surfacing an error, so
// the butler can wait or report rather than aborting (design §5.3).
var ErrNoDevice = errors.New("no online device")

// Runner executes a single adb invocation. argv is the full command line,
// e.g. ["adb", "-s", "emulator-5554", "shell", "input", "tap", "10", "20"].
// Production shells out to the host adb binary; tests substitute a
// fixture-backed fake (design §6.1 fake-adb seam: command → fixture output).
// Implementations must not leak the target serial in returned errors.
type Runner interface {
	Run(ctx context.Context, argv ...string) ([]byte, error)
}

// execRunner is the production Runner. It scrubs the target serial from
// stderr before surfacing any error (no-leak, design §5.4): adb errors can
// name the serial, and stderr text must never reach logs or results.
type execRunner struct{}

// NewRealRunner returns the production Runner that execs the host adb.
func NewRealRunner() Runner { return execRunner{} }

// Run executes argv[0] with argv[1:] under ctx and returns stdout.
func (execRunner) Run(ctx context.Context, argv ...string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("adb: empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	serial := serialFromArgv(argv)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		stderr := strings.TrimSpace(string(ee.Stderr))
		if stderr == "" {
			stderr = err.Error()
		}
		return out, errors.New(firstLine(redact(serial, stderr)))
	}
	return out, errors.New(redact(serial, err.Error()))
}

// serialFromArgv extracts the value following "-s" if present.
func serialFromArgv(argv []string) string {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "-s" {
			return argv[i+1]
		}
	}
	return ""
}

// redact removes serial occurrences from a message; empty serial is a no-op.
func redact(serial, msg string) string {
	if serial == "" {
		return msg
	}
	return strings.ReplaceAll(msg, serial, "device")
}

// firstLine keeps only the first line of a message, capped at 200 bytes.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// ADBExecutor runs adb against one device addressed by its policy alias.
// The serial is resolved internally and used only to build argv; it never
// appears in results, errors, or logs (no-leak, design §5.4).
type ADBExecutor struct {
	alias   string // policy device-alias; the only device name in any output
	runner  Runner
	timeout time.Duration // per-adb-invocation bound
	pin     string        // optional pinned serial (policy device_serial)

	dumpMu  sync.Mutex
	dumpXML string
	dumpAt  time.Time
	dumpTTL time.Duration
}

// NewADBExecutor builds an executor. alias is the policy device-alias (the
// only device identifier allowed in any output); runner is the adb seam;
// timeout bounds each adb call; dumpTTL is the uiautomator-dump cache
// lifetime; pin optionally pins the adb serial (empty = auto-detect).
func NewADBExecutor(alias string, runner Runner, timeout, dumpTTL time.Duration, pin string) *ADBExecutor {
	if timeout <= 0 {
		timeout = DefaultADBTimeout
	}
	if dumpTTL <= 0 {
		dumpTTL = DefaultDumpTTL
	}
	return &ADBExecutor{alias: alias, runner: runner, timeout: timeout, dumpTTL: dumpTTL, pin: pin}
}

// NewRealADBExecutor is the production constructor.
func NewRealADBExecutor(alias, pin string) *ADBExecutor {
	return NewADBExecutor(alias, NewRealRunner(), DefaultADBTimeout, DefaultDumpTTL, pin)
}

// run invokes adb -s <serial> <args...> under the executor timeout.
// On deadline the error message is controlled and carries no serial.
func (e *ADBExecutor) run(ctx context.Context, serial string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	out, err := e.runner.Run(ctx, append([]string{"adb", "-s", serial}, args...)...)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("adb timed out after %s", e.timeout)
		}
		return out, err
	}
	return out, nil
}

// runNoSerial invokes a bare adb command (no -s target), e.g. device
// enumeration, under the executor timeout.
func (e *ADBExecutor) runNoSerial(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	out, err := e.runner.Run(ctx, append([]string{"adb"}, args...)...)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("adb timed out after %s", e.timeout)
		}
		return out, err
	}
	return out, nil
}

// resolveSerial maps the policy alias to an adb transport serial using
// `adb devices` (design §5.4): pinned serial if set, otherwise the single
// online device. It never includes a serial in error text.
func (e *ADBExecutor) resolveSerial(ctx context.Context) (string, error) {
	out, err := e.runNoSerial(ctx, "devices")
	if err != nil {
		return "", fmt.Errorf("listing devices: %w", err)
	}
	serials := onlineDevices(string(out))
	if e.pin != "" {
		for _, s := range serials {
			if s == e.pin {
				return s, nil
			}
		}
		return "", fmt.Errorf("pinned device for alias %q is not online", e.alias)
	}
	switch len(serials) {
	case 0:
		return "", fmt.Errorf("alias %q: %w", e.alias, ErrNoDevice)
	case 1:
		return serials[0], nil
	default:
		return "", fmt.Errorf("alias %q: %d devices online; pin device_serial in the policy file", e.alias, len(serials))
	}
}

// onlineDevices parses `adb devices` output into the list of online serials
// (state "device"); offline/unauthorized entries are ignored.
func onlineDevices(out string) []string {
	serials := make([]string, 0, 4)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "List" || strings.HasPrefix(fields[0], "*") {
			continue
		}
		if fields[1] == "device" {
			serials = append(serials, fields[0])
		}
	}
	return serials
}

// InvalidateDump drops the cached uiautomator dump. Mutating tools call
// this after every tap/type/swipe so the next find sees post-action UI
// (design §4: re-dump after any action).
func (e *ADBExecutor) InvalidateDump() {
	e.dumpMu.Lock()
	defer e.dumpMu.Unlock()
	e.dumpXML = ""
	e.dumpAt = time.Time{}
}
