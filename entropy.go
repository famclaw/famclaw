package main

import (
	"fmt"
	"strings"
	"math"
)

func hasHighEntropySingleLabel(host string) bool {
	// Split host into labels (separated by dots)
	labels := strings.Split(host, ".")
	
	// If there's only one label, check its entropy
	if len(labels) == 1 {
		label := labels[0]
		// If label has ≥24 characters, consider it high entropy
		if len(label) >= 24 {
			return true
		}
	}
	
	// For multi-label hosts, we should not treat them as high entropy
	// The original logic was incorrect - it should only check single labels
	// Return false for multi-label hosts to avoid false positives
	return false
}

// Simple entropy calculation for testing
func calculateEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	
	// Count frequency of each character
	freq := make(map[rune]int)
	for _, r := range s {
		freq[r]++
	}
	
	// Calculate entropy
	var entropy float64
	total := float64(len(s))
	for _, count := range freq {
		p := float64(count) / total
		if p > 0 {
			entropy -= p * math.Log2(p)
		}
	}
	
	return entropy
}

func main() {
	testCases := []struct {
		host string
		expected bool
	}{
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.com", true}, // 26 chars, should be high entropy
		{"example.com", false}, // 7 chars, should not be high entropy
		{"abc.com", false}, // 3 chars, should not be high entropy
	}
	
	for _, tc := range testCases {
		result := hasHighEntropySingleLabel(tc.host)
		entropy := calculateEntropy(strings.Split(tc.host, ".")[0])
		fmt.Printf("Host: %q, Expected: %v, Got: %v, Entropy: %.2f\n", tc.host, tc.expected, result, entropy)
	}
}