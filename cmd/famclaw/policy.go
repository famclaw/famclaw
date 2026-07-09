package main

import (
	"fmt"
	"os"

	"github.com/famclaw/famclaw/internal/policy"
)

func runPolicyCommand(args []string) {
	if len(args) == 0 {
		printPolicyUsage()
		os.Exit(2)
	}

	switch args[0] {
	case "hash":
		policyHash()
	default:
		fmt.Fprintf(os.Stderr, "Unknown policy command: %s\n", args[0])
		printPolicyUsage()
		os.Exit(2)
	}
}

func policyHash() {
	hash, err := policy.ComputeEmbeddedPolicyHash()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(hash)
}

func printPolicyUsage() {
	fmt.Fprintf(os.Stderr, `Usage: famclaw policy <command> [args]

Commands:
  hash              Print the SHA-256 hash of the embedded OPA policy files
`)
}
