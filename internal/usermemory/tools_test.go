package usermemory

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/store"
)

func TestHandleRemember_RejectsControlChars(t *testing.T) {
	cases := []struct{ name, category, label, value string }{
		{"null in label", "prefs", "co\x00ffee", "black"},
		{"unit separator in value", "prefs", "coffee", "black\x1fsugar"},
		{"control char in category", "pr\x07efs", "coffee", "black"},
		{"control char at start of value", "prefs", "coffee", "\x01black"},
		{"control char at end of value", "prefs", "coffee", "black\x1b"},
		{"control char at start of label", "prefs", "\x02coffee", "black"},
		{"control char at end of category", "prefs\x1f", "coffee", "black"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := HandleRemember(context.Background(), nil, "user1", tc.category, tc.label, tc.value)
			if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
			if !strings.Contains(msg, "control character") {
				t.Fatalf("expected control-character rejection, got %q", msg)
			}
		})
	}
}

func TestHandleRecall(t *testing.T) {
	dbPath := "/tmp/usermemory_recall_test_" + time.Now().Format("20060102150405") + ".db"
	defer os.Remove(dbPath)

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	s := NewStore(db)
	ctx := context.Background()

	// Seed two users: alice has memories, bob has one.
	for _, m := range []*Memory{
		{UserName: "alice", Category: "preferences", Label: "coffee", Value: "black, no sugar"},
		{UserName: "alice", Category: "projects", Label: "website", Value: "building a personal site"},
		{UserName: "bob", Category: "preferences", Label: "coffee", Value: "with milk"},
	} {
		if err := s.UpsertMemory(ctx, m); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	cases := []struct {
		name            string
		userName        string
		category        string
		query           string
		wantEqual       string
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:         "no query lists all grouped",
			userName:     "alice",
			query:        "",
			wantContains: []string{"All memories:", "Preferences:", "coffee: black, no sugar", "Projects:", "website: building a personal site"},
		},
		{
			name:            "query by label returns grouped format",
			userName:        "alice",
			query:           "coffee",
			wantContains:    []string{"All memories:", "Preferences:", "coffee: black, no sugar"},
			wantNotContains: []string{"Projects:", "website", "building a personal site"},
		},
		{
			name:         "query by value returns grouped format",
			userName:     "alice",
			query:        "building",
			wantContains: []string{"All memories:", "Projects:", "website: building a personal site"},
		},
		{
			name:      "query no results without category",
			userName:  "alice",
			query:     "nonexistent",
			wantEqual: fmt.Sprintf("No memories matching %q.", "nonexistent"),
		},
		{
			name:      "query no results with category",
			userName:  "alice",
			category:  "projects",
			query:     "coffee",
			wantEqual: fmt.Sprintf("No memories in category %q matching %q.", "projects", "coffee"),
		},
		{
			name:      "no query no category empty",
			userName:  "nobody",
			query:     "",
			wantEqual: "No memories stored yet.",
		},
		{
			name:      "query no memories for user",
			userName:  "nobody",
			query:     "anything",
			wantEqual: fmt.Sprintf("No memories matching %q.", "anything"),
		},
		{
			name:      "query no memories for user with category",
			userName:  "nobody",
			category:  "projects",
			query:     "anything",
			wantEqual: fmt.Sprintf("No memories in category %q matching %q.", "projects", "anything"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := HandleRecall(ctx, s, tc.userName, tc.category, tc.query)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantEqual != "" {
				if msg != tc.wantEqual {
					t.Errorf("expected %q, got %q", tc.wantEqual, msg)
				}
				return
			}
			for _, sub := range tc.wantContains {
				if !strings.Contains(msg, sub) {
					t.Errorf("expected output to contain %q, got %q", sub, msg)
				}
			}
			for _, sub := range tc.wantNotContains {
				if strings.Contains(msg, sub) {
					t.Errorf("expected output to NOT contain %q, got %q", sub, msg)
				}
			}
		})
	}
}
