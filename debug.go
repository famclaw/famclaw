package main

import (
	"fmt"
	"net"
	"strings"
	"strconv"
)

func isIPLiteral(host string) bool {
	// Handle IPv6 bracketed notation like [::1]
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		// Extract the IP part inside brackets
		ipStr := host[1 : len(host)-1]
		ip := net.ParseIP(ipStr)
		return ip != nil
	}

	// Handle IPv6 bare notation (without brackets)
	// We need to check if it's a valid IPv6 address
	if strings.Contains(host, ":") && !strings.Contains(host, ".") {
		// Try parsing as IPv6 without port
		ip := net.ParseIP(host)
		if ip != nil && ip.To16() != nil {
			return true
		}
	}

	// Handle IPv4 and IPv6 with ports
	// Use net.SplitHostPort for proper port-aware parsing
	hostWithoutPort := host
	if strings.Contains(host, ":") {
		// Try to split host and port properly
		if _, _, err := net.SplitHostPort(host); err == nil {
			// Valid host:port format, use SplitHostPort to get host
			hostWithoutPort = host
		} else if idx := strings.LastIndex(host, ":"); idx > 0 {
			// Try to parse as host:port where port might be invalid
			// Check if the part after last colon could be a port
			portPart := host[idx+1:]
			if _, err := strconv.Atoi(portPart); err == nil {
				hostWithoutPort = host[:idx]
			}
		}
	}

	// Try parsing as IP
	ip := net.ParseIP(hostWithoutPort)
	return ip != nil
}

func main() {
	testCases := []string{
		"192.168.1.1",
		"[::1]",
		"192.168.1.1:8080",
		"example.com",
	}
	
	for _, host := range testCases {
		result := isIPLiteral(host)
		fmt.Printf("isIPLiteral(%q) = %v\n", host, result)
	}
}