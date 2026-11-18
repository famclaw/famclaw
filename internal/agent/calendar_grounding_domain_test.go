package agent

import (
	"testing"

	"github.com/famclaw/famclaw/internal/store"
)

// TestCalendarDomainClassification verifies the typed request-domain
// classifier against positive and negative cases, including public-doc
// research, weather, corrections after corrections, and Family follow-up
// after enumeration.
func TestCalendarDomainClassification(t *testing.T) {
	tests := []struct {
		name          string
		userMessage   string
		history       []*store.Message
		wantPersonal  bool
		wantEvent     bool
		wantDiscovery bool
	}{
		{
			name:          "personal calendar event question",
			userMessage:   "What is on my calendar this week?",
			wantPersonal:  true,
			wantEvent:     true,
			wantDiscovery: false,
		},
		{
			name:          "calendar discovery question",
			userMessage:   "Which calendars can you access?",
			wantPersonal:  true,
			wantEvent:     false,
			wantDiscovery: true,
		},
		{
			name:        "correction after calendar event question",
			userMessage: "That is wrong; check again.",
			history: []*store.Message{
				{Role: "user", Content: "What is on my calendar this week?"},
				{Role: "assistant", Content: "You have a meeting on Tuesday."},
				{Role: "user", Content: "That is wrong; check again."},
			},
			wantPersonal:  true,
			wantEvent:     true,
			wantDiscovery: false,
		},
		{
			name:        "Family follow-up after enumeration",
			userMessage: "What is on the Family calendar?",
			history: []*store.Message{
				{Role: "user", Content: "Which calendars can you access?"},
				{Role: "assistant", Content: "Two calendars are available."},
				{Role: "user", Content: "What is on the Family calendar?"},
			},
			wantPersonal:  true,
			wantEvent:     true,
			wantDiscovery: false,
		},
		{
			name:          "public calendar API research",
			userMessage:   "How does the Google Calendar API work?",
			wantPersonal:  false,
			wantEvent:     false,
			wantDiscovery: false,
		},
		{
			name:          "calendar SDK documentation",
			userMessage:   "Show me the calendar documentation for the service account",
			wantPersonal:  false,
			wantEvent:     false,
			wantDiscovery: false,
		},
		{
			name:          "weather today is not calendar",
			userMessage:   "What is the weather today?",
			wantPersonal:  false,
			wantEvent:     false,
			wantDiscovery: false,
		},
		{
			name:          "unrelated what is on",
			userMessage:   "What is on the menu?",
			wantPersonal:  false,
			wantEvent:     false,
			wantDiscovery: false,
		},
		{
			name:        "correction after correction inherits calendar domain",
			userMessage: "Check it again, that is still wrong.",
			history: []*store.Message{
				{Role: "user", Content: "What is on my calendar this week?"},
				{Role: "assistant", Content: "You have a meeting."},
				{Role: "user", Content: "That is wrong; check again."},
				{Role: "assistant", Content: "I cannot verify."},
			},
			wantPersonal:  true,
			wantEvent:     true,
			wantDiscovery: false,
		},
		{
			name:        "weather follow-up after weather is not calendar",
			userMessage: "That is wrong; check again.",
			history: []*store.Message{
				{Role: "user", Content: "What is the weather today?"},
				{Role: "assistant", Content: "It is sunny."},
			},
			wantPersonal:  false,
			wantEvent:     false,
			wantDiscovery: false,
		},
		{
			name:          "my schedule question",
			userMessage:   "What does my schedule look like tomorrow?",
			wantPersonal:  true,
			wantEvent:     true,
			wantDiscovery: false,
		},
		{
			name:          "calendar API oauth question is public research",
			userMessage:   "How do I set up calendar oauth for my app?",
			wantPersonal:  false,
			wantEvent:     false,
			wantDiscovery: false,
		},
		{
			name:          "private calendar check",
			userMessage:   "Check my private calendar.",
			wantPersonal:  true,
			wantEvent:     true,
			wantDiscovery: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			personal := isPersonalCalendarRequest(tt.userMessage, tt.history)
			if personal != tt.wantPersonal {
				t.Errorf("isPersonalCalendarRequest(%q, history=%v) = %v; want %v",
					tt.userMessage, len(tt.history) > 0, personal, tt.wantPersonal)
			}
			event := isCalendarEventRequest(tt.userMessage, tt.history)
			if event != tt.wantEvent {
				t.Errorf("isCalendarEventRequest(%q, history=%v) = %v; want %v",
					tt.userMessage, len(tt.history) > 0, event, tt.wantEvent)
			}
			discovery := calendarDiscoveryOnly(tt.userMessage)
			if discovery != tt.wantDiscovery {
				t.Errorf("calendarDiscoveryOnly(%q) = %v; want %v",
					tt.userMessage, discovery, tt.wantDiscovery)
			}
		})
	}
}
