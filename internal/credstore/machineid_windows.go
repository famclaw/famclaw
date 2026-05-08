//go:build windows

package credstore

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// MachineID returns the MachineGuid value under
// HKLM\SOFTWARE\Microsoft\Cryptography. Windows generates this GUID at OS
// install time and treats it as a stable per-machine identifier.
func MachineID() (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Cryptography`,
		registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", fmt.Errorf("opening registry key: %w", err)
	}
	defer k.Close()
	v, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return "", fmt.Errorf("reading MachineGuid: %w", err)
	}
	return strings.TrimSpace(v), nil
}
