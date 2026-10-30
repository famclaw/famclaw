package webfilter

import (
	"testing"
)

func TestSuspiciousSiteDetector_Check(t *testing.T) {
	detector := NewSuspiciousSiteDetector()

	tests := []struct {
		name     string
		host     string
		expected Verdict
	}{
		{
			name: "IP literal host",
			host: "192.168.1.1",
			expected: Verdict{
				Action: "block",
				Reason: "IP literal host detected",
			},
		},
		{
			name: "IPv6 literal host",
			host: "[::1]",
			expected: Verdict{
				Action: "block",
				Reason: "IP literal host detected",
			},
		},
		{
			name: "Non-standard port",
			host: "example.com:8080",
			expected: Verdict{
				Action: "ask_parent",
				Reason: "non-standard port detected",
			},
		},
		{
			name: "Punycode host",
			host: "example.xn--fsq.xn--fiqs8s",
			expected: Verdict{
				Action: "ask_parent",
				Reason: "punycode or confusable characters detected",
			},
		},
		{
			name: "High entropy single label",
			host: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.com",
			expected: Verdict{
				Action: "ask_parent",
				Reason: "high entropy single label detected",
			},
		},
		{
			name: "Typo squatting",
			host: "googl.com",
			expected: Verdict{
				Action: "ask_parent",
				Reason: "typo-squatting detected",
			},
		},
		{
			name: "Normal host",
			host: "example.com",
			expected: Verdict{
				Action: "warn_allow",
				Reason: "host passed suspicious site checks",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detector.Check(tt.host)
			if result.Action != tt.expected.Action {
				t.Errorf("Check() action = %v, want %v", result.Action, tt.expected.Action)
			}
			if result.Reason != tt.expected.Reason {
				t.Errorf("Check() reason = %v, want %v", result.Reason, tt.expected.Reason)
			}
		})
	}
}

func TestSuspiciousSiteDetector_IPLiteral(t *testing.T) {
	tests := []struct {
		name string
		host string
		want bool
	}{
		{
			name: "IPv4 literal",
			host: "192.168.1.1",
			want: true,
		},
		{
			name: "IPv6 literal",
			host: "[::1]",
			want: true,
		},
		{
			name: "IPv4 with port",
			host: "192.168.1.1:8080",
			want: true,
		},
		{
			name: "Regular host",
			host: "example.com",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isIPLiteral(tt.host); got != tt.want {
				t.Errorf("isIPLiteral() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSuspiciousSiteDetector_NonStandardPort(t *testing.T) {
	tests := []struct {
		name string
		host string
		want bool
	}{
		{
			name: "Standard HTTPS port",
			host: "example.com:443",
			want: false,
		},
		{
			name: "Standard HTTP port",
			host: "example.com:80",
			want: false,
		},
		{
			name: "Non-standard port",
			host: "example.com:8080",
			want: true,
		},
		{
			name: "Regular host without port",
			host: "example.com",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasNonStandardPort(tt.host); got != tt.want {
				t.Errorf("hasNonStandardPort() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSuspiciousSiteDetector_Punycode(t *testing.T) {
	tests := []struct {
		name string
		host string
		want bool
	}{
		{
			name: "Punycode host",
			host: "example.xn--fsq.xn--fiqs8s",
			want: true,
		},
		{
			name: "Regular host",
			host: "example.com",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasPunycodeOrConfusables(tt.host); got != tt.want {
				t.Errorf("hasPunycodeOrConfusables() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSuspiciousSiteDetector_HighEntropy(t *testing.T) {
	tests := []struct {
		name string
		host string
		want bool
	}{
		{
			name: "High entropy single label",
			host: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.com",
			want: true,
		},
		{
			name: "Normal single label",
			host: "example.com",
			want: false,
		},
		{
			name: "Short single label",
			host: "abc.com",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasHighEntropySingleLabel(tt.host); got != tt.want {
				t.Errorf("hasHighEntropySingleLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSuspiciousSiteDetector_TypoSquat(t *testing.T) {
	tests := []struct {
		name string
		host string
		want bool
	}{
		{
			name: "Typo squatting",
			host: "googl.com",
			want: true,
		},
		{
			name: "Regular host",
			host: "google.com",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTypoSquat(tt.host); got != tt.want {
				t.Errorf("isTypoSquat() = %v, want %v", got, tt.want)
			}
		})
	}
}