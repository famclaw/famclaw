package config

import "testing"

func TestNormalizeAllowlistHost(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "bare host", input: "softlandings.com", want: "softlandings.com"},
		{name: "case variant lowercased", input: "EXAMPLE.COM", want: "example.com"},
		{name: "trailing dot stripped", input: "Example.com.", want: "example.com"},
		{name: "surrounding whitespace trimmed", input: "  example.com  ", want: "example.com"},
		{name: "url with scheme", input: "https://softlandings.com", want: "softlandings.com"},
		{name: "url with scheme and path", input: "https://softlandings.com/pages/landing", want: "softlandings.com"},
		{name: "url with port stripped", input: "http://searxng.local:8888/search", want: "searxng.local"},
		{name: "http scheme", input: "http://example.com", want: "example.com"},
		{name: "subdomain kept", input: "blog.example.com", want: "blog.example.com"},
		{name: "single-label host", input: "localhost", want: "localhost"},
		{name: "ipv4 literal", input: "192.168.1.10", want: "192.168.1.10"},
		{name: "ipv4 with port", input: "http://10.0.0.5:11434", want: "10.0.0.5"},
		{name: "hyphenated label", input: "my-site.example", want: "my-site.example"},

		{name: "empty", input: "", wantErr: true},
		{name: "only whitespace", input: "   ", wantErr: true},
		{name: "scheme only", input: "https://", wantErr: true},
		{name: "port but no host", input: "http://:8443", wantErr: true},
		{name: "leading hyphen label", input: "-bad.example.com", wantErr: true},
		{name: "trailing hyphen label", input: "bad-.example.com", wantErr: true},
		{name: "underscore not allowed", input: "ex_ample.com", wantErr: true},
		{name: "spaces inside host", input: "exa mple.com", wantErr: true},
		{name: "ipv6 literal rejected with clear error", input: "[::1]", wantErr: true},
		{name: "too-long label", input: "abcde", want: "abcde"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeAllowlistHost(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeAllowlistHost(%q) = %q, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeAllowlistHost(%q) error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeAllowlistHost(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
