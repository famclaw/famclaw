// Command seccheck is deprecated — use honeybadger scan instead.
// This binary is kept for backward compatibility with existing skill installations.
// It delegates to the honeybadger binary if available, otherwise prints a deprecation notice.
package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: seccheck <repo-url-or-path>\n")
		fmt.Fprintf(os.Stderr, "\nDeprecated: use 'honeybadger scan' instead.\n")
		os.Exit(2)
	}

	// Try to delegate to honeybadger
	hb, err := exec.LookPath("honeybadger")
	if err == nil {
		cmd := exec.Command(hb, append([]string{"scan"}, os.Args[1:]...)...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			os.Exit(3)
		}
		return
	}

	fmt.Fprintf(os.Stderr, "seccheck is deprecated. Install honeybadger:\n")
	fmt.Fprintf(os.Stderr, "  go install github.com/famclaw/honeybadger/cmd/honeybadger@latest\n")
	os.Exit(127) // POSIX: command not found
}
