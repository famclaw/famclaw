//go:build linux

package credstore

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// MachineID returns the contents of /etc/machine-id, stripped of surrounding
// whitespace. systemd guarantees this file is a stable 128-bit identifier
// for the lifetime of the OS install.
func MachineID() (string, error) {
	b, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return "", fmt.Errorf("reading /etc/machine-id: %w", err)
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", errors.New("credstore: /etc/machine-id is empty")
	}
	return id, nil
}
