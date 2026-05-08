//go:build darwin

package credstore

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// MachineID returns the IOPlatformUUID reported by ioreg. This is a stable
// per-machine UUID that survives reinstalls.
func MachineID() (string, error) {
	cmd := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("running ioreg: %w", err)
	}
	// Lines look like:   "IOPlatformUUID" = "XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX"
	// We locate the "=" then take the value between the first/last quote
	// after it.
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "IOPlatformUUID") {
			continue
		}
		eqIdx := strings.Index(line, "=")
		if eqIdx < 0 {
			continue
		}
		rest := line[eqIdx+1:]
		fi := strings.Index(rest, "\"")
		li := strings.LastIndex(rest, "\"")
		if fi < 0 || li <= fi {
			continue
		}
		uuid := strings.TrimSpace(rest[fi+1 : li])
		if uuid != "" {
			return uuid, nil
		}
	}
	return "", errors.New("credstore: IOPlatformUUID not found in ioreg output")
}
