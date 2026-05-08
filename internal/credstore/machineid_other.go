//go:build !linux && !darwin && !windows

package credstore

import "errors"

// MachineID is unsupported on this platform.
func MachineID() (string, error) {
	return "", errors.New("credstore: unsupported platform")
}
