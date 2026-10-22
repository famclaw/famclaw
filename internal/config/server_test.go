package config

import (
	"strings"
	"testing"
)

// validHex is a 64-char hex value (2× openssl rand -hex 32) that passes
// the secret-length check so the table rows below exercise the bind and
// origin-policy rules in isolation.
const validHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestValidate_ServerHost covers the bind matrix (design-note cases T18–T21):
// a wildcard bind requires a 32+ char secret, loopback does not, and a host
// that is not an IP address fails boot with a message naming the key.
func TestValidate_ServerHost(t *testing.T) {
	tests := []struct {
		name        string
		host        string
		secret      string
		wantErrSubs []string // all must appear in the error; nil = expect no error
	}{
		{
			name:        "wildcard 0.0.0.0 without secret",
			host:        "0.0.0.0",
			secret:      "",
			wantErrSubs: []string{"server.host", "all interfaces", "server.secret"},
		},
		{
			name:        "wildcard 0.0.0.0 with valid secret",
			host:        "0.0.0.0",
			secret:      validHex,
			wantErrSubs: nil,
		},
		{
			name:        "wildcard :: without secret",
			host:        "::",
			secret:      "",
			wantErrSubs: []string{"all interfaces", "server.secret"},
		},
		{
			name:        "wildcard * without secret",
			host:        "*",
			secret:      "",
			wantErrSubs: []string{"all interfaces", "server.secret"},
		},
		{
			name:        "loopback 127.0.0.1 without secret",
			host:        "127.0.0.1",
			secret:      "",
			wantErrSubs: nil,
		},
		{
			name:        "loopback ::1 without secret",
			host:        "::1",
			secret:      "",
			wantErrSubs: nil,
		},
		{
			name: "specific non-loopback IP without secret",
			// 10.x is used deliberately — a 192.168/16 household-range address
			// would look like a real LAN address and fail the no-private-info
			// review gate, while 10.x is the canonical RFC1918 test range.
			host:        "10.0.0.50",
			secret:      "",
			wantErrSubs: nil,
		},
		{
			name:        "garbage host",
			host:        "not-an-ip",
			secret:      validHex,
			wantErrSubs: []string{"server.host", "not-an-ip"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{
				// TimeoutSeconds set so the LLM rule can't shadow the bind
				// rule under test (a full Config from Load() carries it).
				Server: ServerConfig{Host: tt.host, Port: 8080, Secret: tt.secret},
				LLM:    LLMConfig{TimeoutSeconds: 300},
			}
			err := c.Validate()
			if tt.wantErrSubs == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error for host=%q secret-len=%d", tt.host, len(tt.secret))
			}
			for _, sub := range tt.wantErrSubs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q does not contain %q", err, sub)
				}
			}
		})
	}
}

// TestValidate_WSOriginPolicy (design-note case T10): the WebSocket origin
// gate accepts the two documented modes (and the pre-default empty string)
// and rejects anything else so a typo cannot silently loosen the check.
func TestValidate_WSOriginPolicy(t *testing.T) {
	tests := []struct {
		name     string
		policy   string
		wantErr  bool
		wantSubs string
	}{
		{name: "default empty (applyDefaults fills same-origin)", policy: "", wantErr: false},
		{name: "same-origin", policy: "same-origin", wantErr: false},
		{name: "strict", policy: "strict", wantErr: false},
		{name: "typo rejected", policy: "sameorigin", wantErr: true, wantSubs: "ws_origin_policy"},
		{name: "arbitrary value rejected", policy: "whatever", wantErr: true, wantSubs: "ws_origin_policy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{
				Server: ServerConfig{Host: "127.0.0.1", Port: 8080, WSOriginPolicy: tt.policy},
				LLM:    LLMConfig{TimeoutSeconds: 300},
			}
			err := c.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Validate() = nil, want error for policy %q", tt.policy)
				}
				if !strings.Contains(err.Error(), tt.wantSubs) {
					t.Errorf("error %q does not name the key %q", err, tt.wantSubs)
				}
			} else if err != nil {
				t.Fatalf("Validate() = %v, want nil for policy %q", err, tt.policy)
			}
		})
	}
}

// TestDescribeBind (design-note case T19, pure-function form): the loud boot
// line names the exposure for wildcard binds, the loopback wording for
// loopback, and the bound address otherwise.
func TestDescribeBind(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{host: "0.0.0.0", want: "listening on all interfaces — LAN/remote access enabled; auth is session-gated (see docs/SECURITY_MODEL.md)"},
		{host: "::", want: "listening on all interfaces — LAN/remote access enabled; auth is session-gated (see docs/SECURITY_MODEL.md)"},
		{host: "*", want: "listening on all interfaces — LAN/remote access enabled; auth is session-gated (see docs/SECURITY_MODEL.md)"},
		{host: "127.0.0.1", want: "listening on loopback only"},
		{host: "::1", want: "listening on loopback only"},
		{host: "10.0.0.50", want: "listening on 10.0.0.50 (bound to a specific interface)"},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := DescribeBind(tt.host); got != tt.want {
				t.Errorf("DescribeBind(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

// TestApplyDefaults_WSOriginPolicy pins the recommended default (design-note
// D2): an unset policy becomes "same-origin", an explicit "strict" survives.
func TestApplyDefaults_WSOriginPolicy(t *testing.T) {
	tests := []struct {
		name   string
		policy string
		want   string
	}{
		{name: "unset defaults to same-origin", policy: "", want: "same-origin"},
		{name: "explicit same-origin preserved", policy: "same-origin", want: "same-origin"},
		{name: "explicit strict preserved", policy: "strict", want: "strict"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{Server: ServerConfig{WSOriginPolicy: tt.policy}}
			applyDefaults(c)
			if c.Server.WSOriginPolicy != tt.want {
				t.Errorf("WSOriginPolicy = %q, want %q", c.Server.WSOriginPolicy, tt.want)
			}
		})
	}
}
