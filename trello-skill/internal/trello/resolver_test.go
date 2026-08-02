package trello

import (
	"strings"
	"testing"
)

func TestResolveList(t *testing.T) {
	r := testResolver()
	tests := []struct {
		name        string
		arg         string
		wantID      string
		wantErr     bool
		errContains string
	}{
		{
			name:   "empty -> default",
			arg:    "",
			wantID: "0123456789abcdef01234567",
		},
		{
			name:   "name resolves (Backlog)",
			arg:    "Backlog",
			wantID: "0123456789abcdef01234567",
		},
		{
			name:   "name resolves (Julia)",
			arg:    "Julia",
			wantID: "0123456789abcdef0123456a",
		},
		{
			name:   "name resolves case-insensitive",
			arg:    "julia",
			wantID: "0123456789abcdef0123456a",
		},
		{
			name:   "name resolves (Done)",
			arg:    "Done",
			wantID: "0123456789abcdef0123456d",
		},
		{
			name:   "known hex id accepted",
			arg:    "0123456789abcdef0123456a",
			wantID: "0123456789abcdef0123456a",
		},
		{
			name:        "unknown name errors",
			arg:         "Backlo",
			wantErr:     true,
			errContains: "not a 24-char hex id",
		},
		{
			name:        "garbage errors",
			arg:         "!!!not-a-list!!!",
			wantErr:     true,
			errContains: "not a 24-char hex id",
		},
		{
			name:        "unknown hex id errors with valid lists",
			arg:         "68e15d40a06fb18420cb0e21",
			wantErr:     true,
			errContains: "valid-format id but is not one of the configured lists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, _, err := r.ResolveList(tt.arg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (resolved to %q)", tt.errContains, id)
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				// Must name the valid lists so the model can self-correct.
				if !strings.Contains(err.Error(), "valid lists") {
					t.Errorf("error %q does not name the valid lists", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tt.wantID {
				t.Errorf("id = %q, want %q", id, tt.wantID)
			}
		})
	}
}

func TestResolveListEmptyMapRequiresHex(t *testing.T) {
	// When TRELLO_LISTS is unset (legacy config), only hex ids are accepted
	// for list_id; a name like "Backlog" cannot be resolved and must error
	// instead of being passed to the API.
	r := NewResolver(Credentials{ListID: "0123456789abcdef01234567"})
	if id, _, err := r.ResolveList(""); err != nil {
		t.Fatalf("empty should default: %v", err)
	} else if id != "0123456789abcdef01234567" {
		t.Errorf("default = %q", id)
	}
	if _, _, err := r.ResolveList("Backlog"); err == nil {
		t.Fatal("expected error for name with no map, got nil")
	}
	// A hex id is trusted when there is no map to validate against.
	if id, _, err := r.ResolveList("0123456789abcdef01234567"); err != nil || id != "0123456789abcdef01234567" {
		t.Errorf("hex id with empty map = %q, err %v", id, err)
	}
}

func TestResolvePerson(t *testing.T) {
	r := testResolver()
	tests := []struct {
		name         string
		person       string
		wantID       string
		wantFallback bool
	}{
		{
			name:   "julia -> her list",
			person: "Julia",
			wantID: "0123456789abcdef0123456a",
		},
		{
			name:   "uppercase julia",
			person: "JULIA",
			wantID: "0123456789abcdef0123456a",
		},
		{
			name:   "lowercase julia",
			person: "julia",
			wantID: "0123456789abcdef0123456a",
		},
		{
			name:   "backlog name",
			person: "Backlog",
			wantID: "0123456789abcdef01234567",
		},
		{
			name:         "empty -> default, no fallback",
			person:       "",
			wantID:       "0123456789abcdef01234567",
			wantFallback: false,
		},
		{
			name:         "unmapped person -> default with fallback",
			person:       "Nobody",
			wantID:       "0123456789abcdef01234567",
			wantFallback: true,
		},
		{
			name:         "garbage -> default with fallback",
			person:       "???",
			wantID:       "0123456789abcdef01234567",
			wantFallback: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, _, fallback, err := r.ResolvePerson(tt.person)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tt.wantID {
				t.Errorf("id = %q, want %q", id, tt.wantID)
			}
			if fallback != tt.wantFallback {
				t.Errorf("fallback = %v, want %v", fallback, tt.wantFallback)
			}
		})
	}
}

func TestResolvePersonNoDefaultErrors(t *testing.T) {
	// A resolver with no default list cannot route an empty/unmapped person.
	r := NewResolver(Credentials{
		Lists: map[string]string{"Julia": "0123456789abcdef0123456a"},
	})
	if _, _, _, err := r.ResolvePerson(""); err == nil {
		t.Fatal("expected error when no default list configured")
	}
}

func TestNewResolverDefaults(t *testing.T) {
	// When TRELLO_LIST_ID is unset, the default comes from the map.
	r := NewResolver(Credentials{
		Lists: map[string]string{
			"Backlog": "0123456789abcdef01234567",
			"Done":    "0123456789abcdef0123456d",
		},
	})
	if r.DefaultListID != "0123456789abcdef01234567" {
		t.Errorf("DefaultListID = %q", r.DefaultListID)
	}
	if r.DoneListID != "0123456789abcdef0123456d" {
		t.Errorf("DoneListID = %q", r.DoneListID)
	}
}

func TestCleanTitle(t *testing.T) {
	r := testResolver()
	tests := []struct {
		title string
		want  string
	}{
		{"create trello token (For Julia)", "create trello token"},
		{"Buy milk", "Buy milk"},
		{"Task (for julia)", "Task"}, // case-insensitive, lowercase 'for'
		{"Task (For Julia) ", "Task"},
		{"Task (For Unknown)", "Task (For Unknown)"}, // unknown name -> not stripped
		{"", ""},
	}
	for _, tt := range tests {
		got := cleanTitle(tt.title, r.Lists)
		if got != tt.want {
			t.Errorf("cleanTitle(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}
