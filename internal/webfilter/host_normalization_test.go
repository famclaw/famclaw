package webfilter

import (
	"reflect"
	"testing"
)

func TestDetectorMixedCaseKnownAndTypos(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"Google.COM", "warn_allow"},
		{"gOOgle.com", "warn_allow"},
		{"Googl.COM", "ask_parent"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			if got := NewSuspiciousSiteDetector().Check(tc.host).Action; got != tc.want {
				t.Fatalf("action=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestParseBaselineIPAndPorts(t *testing.T) {
	got := parseBaseline("::1\n[2001:db8::1]\n[2001:db8::2]:443\nExample.COM:443\nExample.org.\ninvalid:bad:host\nempty-port.example:\n")
	want := map[string]struct{}{"::1": {}, "2001:db8::1": {}, "2001:db8::2": {}, "example.com": {}, "example.org": {}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed baseline=%v want=%v", got, want)
	}
}
