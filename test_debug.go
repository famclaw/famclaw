package main

import (
	"fmt"
	"strings"
)

// Simulating the exact function from the test
func hasHighEntropySingleLabel(host string) bool {
	// Split host into labels (separated by dots)
	labels := strings.Split(host, ".")
	fmt.Printf("Host: %q -> labels: %v\n", host, labels)
	
	// If there's only one label, check its entropy
	if len(labels) == 1 {
		label := labels[0]
		fmt.Printf("Single label: %q, Length: %d\n", label, len(label))
		// If label has ≥24 characters, consider it high entropy
		if len(label) >= 24 {
			fmt.Printf("Label has %d chars >= 24, returning true\n", len(label))
			return true
		}
	}
	
	fmt.Printf("Not a single label or not long enough, returning false\n")
	return false
}

func main() {
	// This is what the test is calling
	host := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.com"
	result := hasHighEntropySingleLabel(host)
	fmt.Printf("Test case result: %v\n", result)
	
	// Let's also check what the original implementation was doing
	// If it's "example.com" it should be false
	host2 := "example.com"
	result2 := hasHighEntropySingleLabel(host2)
	fmt.Printf("Example case result: %v\n", result2)
}