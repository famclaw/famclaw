package usermemory

import (
	"context"
	"strings"
	"testing"
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