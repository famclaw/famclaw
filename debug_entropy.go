package main

import (
	"fmt"
	"strings"
)

func hasHighEntropySingleLabel(host string) bool {
	// Split host into labels (separated by dots)
	labels := strings.Split(host, ".")
	
	fmt.Printf("Host: %q, Labels: %v\n", host, labels)
	
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
	host := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.com"
	result := hasHighEntropySingleLabel(host)
	fmt.Printf("Final result for %q: %v\n", host, result)
}