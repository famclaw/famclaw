package reminder

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// --- test helpers ---

func newTestDB(t *testing.T) (*store.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return db, func() { _ = db.Close() }
}

// linkUser creates a message + gateway account so MostRecentGatewayAndExternalIDForUser
// can resolve targetUser's destination.
func linkUser(t *testing.T, db *store.DB, userName, gateway, externalID string) {
	t.Helper()
	convID := "conv-" + userName
	if err := db.SaveMessage(convID, userName, "user", "hi", "", "", gateway); err != nil {
		t.Fatalf("SaveMessage for %s: %v", userName, err)
	}
	if err := db.LinkGatewayAccount(userName, gateway, externalID); err != nil {
		t.Fatalf("LinkGatewayAccount for %s: %v", userName, err)
	}
}

func parentCfg() *config.Config {
	return &config.Config{
		Users: []config.UserConfig{
			{Name: "mom", Role: "parent", AgeGroup: ""},
			{Name: "julia", Role: "child", AgeGroup: "age_13_17"},
		},
	}
}

func childCfg() *config.Config {
	return &config.Config{
		Users: []config.UserConfig{
			{Name: "julia", Role: "child", AgeGroup: "age_13_17"},
			{Name: "mom", Role: "parent", AgeGroup: ""},
		},
	}
}

func parentUser() *config.UserConfig {
	return &config.UserConfig{Name: "mom", Role: "parent", AgeGroup: ""}
}

func childUser() *config.UserConfig {
	return &config.UserConfig{Name: "julia", Role: "child", AgeGroup: "age_13_17"}
}

// --- table-driven tests ---

func TestHandleAddReminder(t *testing.T) {
	cases := []struct {
		name string
		// setup
		user     *config.UserConfig
		cfg      *config.Config
		forUser  string
		linkTarget bool // if true, link the forUser to a gateway before calling
		targetGW   string
		targetID   string
		// input
		when    string
		message string
		// expectations
		wantErr     bool
		wantErrSub  string
		wantForUser string
	}{
		{
			name:         "self reminder parent",
			user:         parentUser(),
			cfg:          parentCfg(),
			forUser:      "",
			when:         "in 10 minutes",
			message:      "buy milk",
			wantForUser:  "mom",
			wantErr:      false,
		},
		{
			name:         "self reminder child",
			user:         childUser(),
			cfg:          childCfg(),
			forUser:      "",
			when:         "in 30 minutes",
			message:      "homework",
			wantForUser:  "julia",
			wantErr:      false,
		},
		{
			name:         "parent sets reminder for child",
			user:         parentUser(),
			cfg:          parentCfg(),
			forUser:      "julia",
			linkTarget:   true,
			targetGW:     "telegram",
			targetID:     "julia-chat-123",
			when:         "in 2 hours",
			message:      "create trello API token",
			wantForUser:  "julia",
			wantErr:      false,
		},
		{
			name:        "child sets reminder for parent — blocked by role",
			user:        childUser(),
			cfg:         childCfg(),
			forUser:     "mom",
			when:        "in 1 hour",
			message:     "do something",
			wantErr:     true,
			wantErrSub:  "only parents can set reminders for other users",
		},
		{
			name:        "unknown target — not a family member",
			user:        parentUser(),
			cfg:         parentCfg(),
			forUser:     "stranger",
			when:        "in 1 hour",
			message:     "hi",
			wantErr:     true,
			wantErrSub:  "is not a configured family member",
		},
		{
			name:        "known target but no gateway recorded",
			user:        parentUser(),
			cfg:         parentCfg(),
			forUser:     "julia",
			linkTarget:  false,
			when:        "in 1 hour",
			message:     "hi",
			wantErr:     true,
			wantErrSub:  "has not sent any messages yet",
		},
		{
			name:        "past time — rejected",
			user:        parentUser(),
			cfg:         parentCfg(),
			when:        "in -1 minutes",
			message:     "should fail",
			wantErr:     true,
			wantErrSub:  "invalid time",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, cleanup := newTestDB(t)
			defer cleanup()
			ctx := context.Background()

			// Link the originating user (self-reminder needs their own gateway)
			linkUser(t, db, "mom", "telegram", "mom-chat")
			linkUser(t, db, "julia", "telegram", "julia-chat-123")

			// For cross-user tests, if linkTarget is false, delete julia's
			// messages/accounts to simulate "no gateway recorded."
			if tc.forUser != "" && !tc.linkTarget {
				// Delete julia's messages and accounts
				_, _ = db.SQL().ExecContext(ctx, "DELETE FROM messages WHERE conversation_id LIKE 'conv-julia%'")
				_, _ = db.SQL().ExecContext(ctx, "DELETE FROM gateway_accounts WHERE user_name = 'julia'")
			}

			_, err := HandleAddReminder(ctx, db, tc.cfg, tc.user,
				"telegram", "mom-chat", "", false,
				tc.when, tc.message, tc.forUser)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrSub)
				}
				if !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestHandleAddReminderCrossUserResolvesGateway verifies that a cross-user
// reminder is stored with the target user's gateway + external_id, not the
// setter's.
func TestHandleAddReminderCrossUserResolvesGateway(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// mom sends from telegram chat "mom-chat"
	linkUser(t, db, "mom", "telegram", "mom-chat")
	// julia sends from discord chat "julia-disc"
	linkUser(t, db, "julia", "discord", "julia-disc")

	// mom sets a reminder for julia
	result, err := HandleAddReminder(ctx, db, parentCfg(), parentUser(),
		"telegram", "mom-chat", "", false,
		"in 1 hour", "create trello API token", "julia")
	if err != nil {
		t.Fatalf("HandleAddReminder: %v", err)
	}

	// Verify the reminder was stored with julia's gateway + external_id
	reminders, err := db.GetPendingReminders(ctx)
	if err != nil {
		t.Fatalf("GetPendingReminders: %v", err)
	}
	if len(reminders) != 1 {
		t.Fatalf("expected 1 reminder, got %d", len(reminders))
	}
	r := reminders[0]
	if r.UserName != "julia" {
		t.Errorf("UserName = %q, want %q", r.UserName, "julia")
	}
	if r.Gateway != "discord" {
		t.Errorf("Gateway = %q, want %q (julia's gateway, not mom's)", r.Gateway, "discord")
	}
	if r.ExternalID != "julia-disc" {
		t.Errorf("ExternalID = %q, want %q (julia's external_id, not mom's)", r.ExternalID, "julia-disc")
	}
	if r.IsGroup {
		t.Error("IsGroup should be false for direct user reminder")
	}

	// Verify the result JSON mentions the target user
	if !strings.Contains(result, "julia") {
		t.Errorf("result should mention target user: %s", result)
	}
}

// TestHandleAddReminderFutureDueTime verifies the reminder is stored with a
// due time in the future.
func TestHandleAddReminderFutureDueTime(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	linkUser(t, db, "mom", "telegram", "mom-chat")

	_, err := HandleAddReminder(ctx, db, parentCfg(), parentUser(),
		"telegram", "mom-chat", "", false,
		"in 5 minutes", "test reminder", "")
	if err != nil {
		t.Fatalf("HandleAddReminder: %v", err)
	}

	reminders, err := db.GetPendingReminders(ctx)
	if err != nil {
		t.Fatalf("GetPendingReminders: %v", err)
	}
	if len(reminders) != 1 {
		t.Fatalf("expected 1 reminder, got %d", len(reminders))
	}
	r := reminders[0]
	now := time.Now()
	if r.DueAt.Before(now) {
		t.Errorf("DueAt = %v, should be in the future (now=%v)", r.DueAt, now)
	}
	if !r.DueAt.After(now) {
		t.Errorf("DueAt = %v should be strictly after now=%v", r.DueAt, now)
	}
}

// TestToolRegistration verifies that reminder.Tool() returns a properly
// configured tool definition.
func TestToolRegistration(t *testing.T) {
	tc := Tool()

	if tc.Name != ToolName {
		t.Errorf("Name = %q, want %q", tc.Name, ToolName)
	}
	if tc.Source != "builtin" {
		t.Errorf("Source = %q, want %q", tc.Source, "builtin")
	}
	// Both parent and child roles should be allowed
	roles := map[string]bool{}
	for _, r := range tc.Roles {
		roles[r] = true
	}
	if !roles["parent"] {
		t.Error("expected 'parent' in Roles")
	}
	if !roles["child"] {
		t.Error("expected 'child' in Roles")
	}

	// Required fields should be "when" and "message"
	required, ok := tc.InputSchema["required"].([]string)
	if !ok {
		t.Fatal("required field not []string")
	}
	if len(required) != 2 {
		t.Fatalf("expected 2 required fields, got %d", len(required))
	}
}

// TestHandleAddReminderCrossUserStoresBothGateways verifies that a cross-user
// reminder stores the TARGET's gateway/external_id in the delivery fields
// (Gateway/ExternalID) and the SETTER's gateway/external_id in the
// SetterGateway/SetterExternalID fields — never the same pair.
func TestHandleAddReminderCrossUserStoresBothGateways(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Mom (setter) sends from Telegram.
	linkUser(t, db, "mom", "telegram", "mom-chat-id")
	// Julia (target) sends from Discord.
	linkUser(t, db, "julia", "discord", "julia-disc-id")

	// Mom sets a reminder for Julia.
	_, err := HandleAddReminder(ctx, db, parentCfg(), parentUser(),
		"telegram", "mom-chat-id", "", false,
		"in 1 hour", "create trello API token", "julia")
	if err != nil {
		t.Fatalf("HandleAddReminder: %v", err)
	}

	reminders, err := db.GetPendingReminders(ctx)
	if err != nil {
		t.Fatalf("GetPendingReminders: %v", err)
	}
	if len(reminders) != 1 {
		t.Fatalf("expected 1 reminder, got %d", len(reminders))
	}
	r := reminders[0]

	// Delivery fields must point to the TARGET (Julia's Discord).
	if r.Gateway != "discord" {
		t.Errorf("Gateway = %q, want %q (target's gateway, not setter's)", r.Gateway, "discord")
	}
	if r.ExternalID != "julia-disc-id" {
		t.Errorf("ExternalID = %q, want %q (target's external_id)", r.ExternalID, "julia-disc-id")
	}

	// Setter fields must point to the SETTER (Mom's Telegram).
	if r.SetterGateway != "telegram" {
		t.Errorf("SetterGateway = %q, want %q (setter's gateway, not target's)", r.SetterGateway, "telegram")
	}
	if r.SetterExternalID != "mom-chat-id" {
		t.Errorf("SetterExternalID = %q, want %q (setter's external_id)", r.SetterExternalID, "mom-chat-id")
	}

	// The two pairs must be distinct.
	if r.Gateway == r.SetterGateway && r.ExternalID == r.SetterExternalID {
		t.Error("delivery and setter routing must not be the same pair")
	}
}
