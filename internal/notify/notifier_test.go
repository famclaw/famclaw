package notify

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

type mockNotifier struct {
	notifyCalled   atomic.Int32
	decisionCalled atomic.Int32
	shouldErr      bool
}

func (m *mockNotifier) Notify(ctx context.Context, a *store.Approval, approveURL, denyURL string) error {
	m.notifyCalled.Add(1)
	if m.shouldErr {
		return fmt.Errorf("mock error")
	}
	return nil
}

func (m *mockNotifier) NotifyDecision(ctx context.Context, a *store.Approval) error {
	m.decisionCalled.Add(1)
	if m.shouldErr {
		return fmt.Errorf("mock error")
	}
	return nil
}

var testApproval = &store.Approval{
	ID:          "test-123",
	UserName:    "alice",
	UserDisplay: "Alice",
	AgeGroup:    "age_8_12",
	Category:    "social_media",
	QueryText:   "Can I make an Instagram account?",
	Status:      "pending",
}

func TestGenerateToken(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		action string
		secret string
	}{
		{"basic approve", "req-1", "approve", "s3cret"},
		{"basic deny", "req-1", "deny", "s3cret"},
		{"empty id", "", "approve", "s3cret"},
		{"empty secret", "req-1", "approve", ""},
		{"long values", "a-very-long-request-id-12345", "approve", "a-very-long-secret-key-67890"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := GenerateToken(tt.id, tt.action, tt.secret)
			if token == "" {
				t.Error("token should not be empty")
			}
			if len(token) != 64 {
				t.Errorf("token length = %d, want 64", len(token))
			}
		})
	}
}

func TestGenerateTokenDeterministic(t *testing.T) {
	t1 := GenerateToken("req-1", "approve", "secret")
	t2 := GenerateToken("req-1", "approve", "secret")
	if t1 != t2 {
		t.Error("same inputs should produce same token")
	}
}

func TestGenerateTokenDifferentActions(t *testing.T) {
	approve := GenerateToken("req-1", "approve", "secret")
	deny := GenerateToken("req-1", "deny", "secret")
	if approve == deny {
		t.Error("different actions should produce different tokens")
	}
}

func TestGenerateTokenDifferentSecrets(t *testing.T) {
	t1 := GenerateToken("req-1", "approve", "secret1")
	t2 := GenerateToken("req-1", "approve", "secret2")
	if t1 == t2 {
		t.Error("different secrets should produce different tokens")
	}
}

func TestGenerateTokenDifferentIDs(t *testing.T) {
	t1 := GenerateToken("req-1", "approve", "secret")
	t2 := GenerateToken("req-2", "approve", "secret")
	if t1 == t2 {
		t.Error("different IDs should produce different tokens")
	}
}

func TestVerifyToken(t *testing.T) {
	token := GenerateToken("req-1", "approve", "secret")

	tests := []struct {
		name   string
		id     string
		action string
		secret string
		valid  bool
	}{
		{"valid token", "req-1", "approve", "secret", true},
		{"wrong action", "req-1", "deny", "secret", false},
		{"wrong id", "req-2", "approve", "secret", false},
		{"wrong secret", "req-1", "approve", "wrong", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := VerifyToken(tt.id, tt.action, token, tt.secret)
			if got != tt.valid {
				t.Errorf("VerifyToken() = %v, want %v", got, tt.valid)
			}
		})
	}
}

func TestMultiNotifierDispatch(t *testing.T) {
	m1 := &mockNotifier{}
	m2 := &mockNotifier{}
	mn := &MultiNotifier{channels: []Notifier{m1, m2}}

	mn.Notify(context.Background(), testApproval, "http://approve", "http://deny")

	if m1.notifyCalled.Load() != 1 {
		t.Error("channel 1 should be called once")
	}
	if m2.notifyCalled.Load() != 1 {
		t.Error("channel 2 should be called once")
	}
}

func TestMultiNotifierDecision(t *testing.T) {
	m1 := &mockNotifier{}
	m2 := &mockNotifier{}
	mn := &MultiNotifier{channels: []Notifier{m1, m2}}

	mn.NotifyDecision(context.Background(), testApproval)

	if m1.decisionCalled.Load() != 1 {
		t.Error("channel 1 decision should be called once")
	}
	if m2.decisionCalled.Load() != 1 {
		t.Error("channel 2 decision should be called once")
	}
}

func TestMultiNotifierErrorDoesNotBlock(t *testing.T) {
	failing := &mockNotifier{shouldErr: true}
	working := &mockNotifier{}
	mn := &MultiNotifier{channels: []Notifier{failing, working}}

	mn.Notify(context.Background(), testApproval, "http://approve", "http://deny")

	if working.notifyCalled.Load() != 1 {
		t.Error("working channel should still be called even when other fails")
	}
}

func TestMultiNotifierEmpty(t *testing.T) {
	mn := &MultiNotifier{}
	mn.Notify(context.Background(), testApproval, "http://approve", "http://deny")
	mn.NotifyDecision(context.Background(), testApproval)
}

func TestNewMultiNotifierNoChannelsEnabled(t *testing.T) {
	cfg := config.NotificationsConfig{}
	mn := NewMultiNotifier(cfg, "secret")
	if len(mn.channels) != 0 {
		t.Errorf("no channels enabled, got %d", len(mn.channels))
	}
}

func TestNewMultiNotifierWithChannels(t *testing.T) {
	cfg := config.NotificationsConfig{
		Slack: config.SlackConfig{Enabled: true, WebhookURL: "http://slack"},
		Ntfy:  config.NtfyConfig{Enabled: true, URL: "http://ntfy", Topic: "test"},
	}
	mn := NewMultiNotifier(cfg, "secret")
	if len(mn.channels) != 2 {
		t.Errorf("expected 2 channels, got %d", len(mn.channels))
	}
}
