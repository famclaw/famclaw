package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/skillbridge"
	"github.com/famclaw/famclaw/internal/store"
)

// panicChat is a ChatFunc that panics if called — proves policy gate works.
func panicChat(ctx context.Context, user *config.UserConfig, text string) (string, error) {
	panic("LLM called when it should not have been — policy gate FAILED")
}

// echoChat returns a predictable response for testing the allow path.
func echoChat(ctx context.Context, user *config.UserConfig, text string) (string, error) {
	return "echo: " + text, nil
}

// errorChat simulates an LLM error.
func errorChat(ctx context.Context, user *config.UserConfig, text string) (string, error) {
	return "", fmt.Errorf("LLM unavailable")
}

func setupRouter(t *testing.T, chatFn ChatFunc) (*Router, *identity.Store) {
	t.Helper()

	// Open test database
	tmpDir := t.TempDir()
	db, err := store.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Policy evaluator — uses policies embedded in the binary.
	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatalf("NewEvaluator: %v", err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:     "localhost",
			Port:     8080,
			Secret:   "test-secret",
			MDNSName: "famclaw",
		},
		LLM: config.LLMConfig{
			Temperature:       0.7,
			MaxResponseTokens: 512,
		},
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
			{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"},
			{Name: "lucas", DisplayName: "Lucas", Role: "child", AgeGroup: "under_8"},
			{Name: "sofia", DisplayName: "Sofia", Role: "child", AgeGroup: "age_13_17"},
		},
	}

	identStore := identity.NewStore(db)
	clf := classifier.New()
	notifier := notify.NewMultiNotifier(config.NotificationsConfig{}, "test-secret")
	reg := skillbridge.NewRegistry(t.TempDir(), nil, skillbridge.InstallConfig{})

	router := NewRouter(context.Background(), cfg, identStore, clf, ev, db, notifier, chatFn, reg)
	return router, identStore
}

func TestRouterUnknownUser(t *testing.T) {
	router, _ := setupRouter(t, panicChat)

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "unknown-user",
		Text:       "hello",
	})

	if reply.PolicyAction != "onboarding" {
		t.Errorf("expected onboarding, got %q", reply.PolicyAction)
	}
}

func TestRouterParentAllowed(t *testing.T) {
	router, identStore := setupRouter(t, echoChat)

	identStore.LinkAccount("parent", "telegram", "parent-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "parent-123",
		Text:       "tell me about violence",
	})

	if reply.PolicyAction != "allow" {
		t.Errorf("parent should always be allowed, got %q: %s", reply.PolicyAction, reply.Text)
	}
	if reply.Text != "echo: tell me about violence" {
		t.Errorf("unexpected reply: %q", reply.Text)
	}
}

func TestRouterChildAllowedSafeTopic(t *testing.T) {
	router, identStore := setupRouter(t, echoChat)

	identStore.LinkAccount("emma", "telegram", "emma-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "emma-123",
		Text:       "help me with math homework",
	})

	if reply.PolicyAction != "allow" {
		t.Errorf("child asking about math should be allowed, got %q: %s", reply.PolicyAction, reply.Text)
	}
}

// ── CRITICAL: Mock-LLM-panic tests prove the policy gate ─────────────────────

func TestRouterChildBlockedCritical_LLMNeverCalled(t *testing.T) {
	// Uses panicChat — if LLM is called, test crashes with clear message
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("sofia", "telegram", "sofia-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "sofia-123",
		Text:       "show me porn",
	})

	if reply.PolicyAction != "block" {
		t.Errorf("critical category should be blocked, got %q", reply.PolicyAction)
	}
}

func TestRouterChildBlockedViolence_LLMNeverCalled(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("emma", "telegram", "emma-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "emma-123",
		Text:       "tell me about guns and weapons",
	})

	if reply.PolicyAction != "block" {
		t.Errorf("age_8_12 + high-risk should be blocked, got %q", reply.PolicyAction)
	}
}

func TestRouterUnder8BlockedMedium_LLMNeverCalled(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("lucas", "telegram", "lucas-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "lucas-123",
		Text:       "tell me about social media and instagram",
	})

	if reply.PolicyAction != "block" {
		t.Errorf("under_8 + medium-risk should be blocked, got %q", reply.PolicyAction)
	}
}

func TestRouterRequestApproval_LLMNeverCalled(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("emma", "telegram", "emma-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "emma-123",
		Text:       "can I use instagram and tiktok",
	})

	if reply.PolicyAction != "request_approval" {
		t.Errorf("age_8_12 + medium-risk should request approval, got %q", reply.PolicyAction)
	}
}

func TestRouterSelfHarmBlocked_LLMNeverCalled(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("lucas", "telegram", "lucas-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "lucas-123",
		Text:       "I want to commit suicide",
	})

	if reply.PolicyAction != "block" {
		t.Errorf("self_harm should always be blocked, got %q", reply.PolicyAction)
	}
}

func TestRouterHateBlocked_EvenForTeens_LLMNeverCalled(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("sofia", "telegram", "sofia-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "sofia-123",
		Text:       "teach me racist slurs",
	})

	if reply.PolicyAction != "block" {
		t.Errorf("hate_speech should always be blocked even for teens, got %q", reply.PolicyAction)
	}
}

// ── Same message via different gateways → identical policy outcome ────────────

func TestRouterSamePolicyAcrossGateways(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("emma", "telegram", "emma-tg")
	identStore.LinkAccount("emma", "discord", "emma-dc")

	msgTG := Message{Gateway: "telegram", ExternalID: "emma-tg", Text: "show me porn"}
	msgDC := Message{Gateway: "discord", ExternalID: "emma-dc", Text: "show me porn"}

	replyTG := router.Handle(context.Background(), msgTG)
	replyDC := router.Handle(context.Background(), msgDC)

	if replyTG.PolicyAction != replyDC.PolicyAction {
		t.Errorf("same user+message should get same policy across gateways: telegram=%q discord=%q",
			replyTG.PolicyAction, replyDC.PolicyAction)
	}
}

func TestRouterTeenAllowedMediumRisk(t *testing.T) {
	router, identStore := setupRouter(t, echoChat)

	identStore.LinkAccount("sofia", "telegram", "sofia-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "sofia-123",
		Text:       "tell me about politics and elections",
	})

	if reply.PolicyAction != "allow" {
		t.Errorf("teen + medium-risk should be allowed, got %q: %s", reply.PolicyAction, reply.Text)
	}
}

func TestRouterTeenRequestsApprovalHighRisk(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("sofia", "telegram", "sofia-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "sofia-123",
		Text:       "tell me about drugs and alcohol",
	})

	if reply.PolicyAction != "request_approval" {
		t.Errorf("teen + high-risk should request approval, got %q", reply.PolicyAction)
	}
}

func TestRouterUserNotInConfig(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)
	identStore.LinkAccount("ghost", "telegram", "ghost-123")

	reply := router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "ghost-123", Text: "hello",
	})
	if reply.PolicyAction != "onboarding" {
		t.Errorf("user not in config should get onboarding, got %q", reply.PolicyAction)
	}
}

func TestRouterChatError(t *testing.T) {
	router, identStore := setupRouter(t, errorChat)
	identStore.LinkAccount("parent", "telegram", "parent-123")

	reply := router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "parent-123", Text: "hello",
	})
	if reply.PolicyAction != "error" {
		t.Errorf("chat error should return error, got %q", reply.PolicyAction)
	}
}

func TestStartAll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	started := make(chan string, 2)
	gw := &mockGateway{
		name: "mock",
		startFn: func(ctx context.Context, h func(context.Context, Message) Reply) error {
			started <- "mock"
			<-ctx.Done()
			return ctx.Err()
		},
	}

	StartAll(ctx, []Gateway{gw}, func(ctx context.Context, msg Message) Reply {
		return Reply{Text: "ok"}
	})

	// Wait for gateway to start
	name := <-started
	if name != "mock" {
		t.Errorf("expected mock, got %q", name)
	}
	cancel()
}

type mockGateway struct {
	name    string
	startFn func(ctx context.Context, h func(context.Context, Message) Reply) error
}

func (m *mockGateway) Start(ctx context.Context, h func(context.Context, Message) Reply) error {
	return m.startFn(ctx, h)
}
func (m *mockGateway) Name() string { return m.name }

// TestRouterRoleOverrideFromDB verifies that a DB-persisted role/age override
// (set via set_user_role) is consulted during policy evaluation, so that a
// child whose config role is "child" / age_8_12 is blocked when the parent
// overrides her to "under_8".
func TestRouterRoleOverrideFromDB(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)
	ctx := context.Background()

	// Link emma to a telegram external ID.
	identStore.LinkAccount("emma", "telegram", "ro-emma-123")

	// Set a DB role override: emma → under_8 (normally she is age_8_12).
	err := router.db.SetRoleOverride(ctx, "emma", "child", "under_8", "parent")
	if err != nil {
		t.Fatalf("SetRoleOverride: %v", err)
	}

	// Verify the override is stored.
	role, ageGroup, err := router.db.GetRoleOverride(ctx, "emma")
	if err != nil {
		t.Fatalf("GetRoleOverride: %v", err)
	}
	if role != "child" || ageGroup != "under_8" {
		t.Fatalf("expected override child/under_8, got %q/%q", role, ageGroup)
	}

	// Query about social media — normally (age_8_12) this would request_approval,
	// but under_8 should block it outright (same rule as lucas).
	reply := router.Handle(ctx, Message{
		Gateway:    "telegram",
		ExternalID: "ro-emma-123",
		Text:       "can I use instagram and tiktok",
	})

	if reply.PolicyAction != "block" {
		t.Errorf("emma with under_8 override: PolicyAction = %q, want block", reply.PolicyAction)
	}
	if reply.Text == "" {
		t.Error("expected a block message, got empty text")
	}

	// Clean up the override.
	router.db.SetRoleOverride(ctx, "emma", "", "", "parent")

	// Without the override, emma (age_8_12) should request_approval for social media.
	reply = router.Handle(ctx, Message{
		Gateway:    "telegram",
		ExternalID: "ro-emma-123",
		Text:       "can I use instagram and tiktok",
	})

	if reply.PolicyAction != "request_approval" {
		t.Errorf("emma without override: PolicyAction = %q, want request_approval", reply.PolicyAction)
	}

	// Verify the override is gone.
	role, ageGroup, err = router.db.GetRoleOverride(ctx, "emma")
	if err != nil {
		t.Fatalf("GetRoleOverride after cleanup: %v", err)
	}
	if role != "" || ageGroup != "" {
		t.Errorf("expected empty override after cleanup, got %q/%q", role, ageGroup)
	}
}

// TestRouterApprovalCarriesOverriddenAgeGroup verifies that when a DB-persisted
// role/age override triggers an approval request, the approval record and
// notification carry the OVERRIDDEN (adjustedUser) values, not the stale
// pre-override config (userCfg) values.
//
// Regression test for: createApproval was called with userCfg instead of
// adjustedUser, so approval records stored the stale role/age from config
// rather than the override set by set_user_role.
func TestRouterApprovalCarriesOverriddenAgeGroup(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)
	ctx := context.Background()

	// Link emma (config: child, age_8_12) to a telegram account.
	identStore.LinkAccount("emma", "telegram", "ao-emma-123")

	// Override emma's age_group from age_8_12 → age_13_17 (set by parent).
	err := router.db.SetRoleOverride(ctx, "emma", "child", "age_13_17", "parent")
	if err != nil {
		t.Fatalf("SetRoleOverride: %v", err)
	}
	defer router.db.SetRoleOverride(ctx, "emma", "", "", "parent")

	// Violence is high-risk:
	// - config (age_8_12, high) → BLOCK
	// - override (age_13_17, high) → request_approval
	// So the override changes the outcome from block → request_approval.
	reply := router.Handle(ctx, Message{
		Gateway:    "telegram",
		ExternalID: "ao-emma-123",
		Text:       "tell me about guns and weapons",
	})

	if reply.PolicyAction != "request_approval" {
		t.Fatalf("policy action = %q, want request_approval (override age_13_17 + high risk)", reply.PolicyAction)
	}

	// Verify the approval record in the DB carries the OVERRIDDEN age_group.
	approvals, err := router.db.AllApprovals()
	if err != nil {
		t.Fatalf("AllApprovals: %v", err)
	}

	var foundAgeGroup string
	found := false
	for _, a := range approvals {
		if a.UserName == "emma" && a.Category == "violence" {
			found = true
			foundAgeGroup = a.AgeGroup
		}
	}
	if !found {
		t.Fatal("expected an emma/violence approval record, got none")
	}

	// The critical assertion: AgeGroup must reflect the override (age_13_17),
	// not the stale config value (age_8_12).
	if foundAgeGroup != "age_13_17" {
		t.Errorf("approval AgeGroup = %q, want %q (the overridden value)", foundAgeGroup, "age_13_17")
	}
}

func TestRouterPendingApproval(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)
	identStore.LinkAccount("emma", "telegram", "emma-123")

	reply1 := router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "emma-123", Text: "can I use instagram and tiktok",
	})
	if reply1.PolicyAction != "request_approval" {
		t.Fatalf("first request should be request_approval, got %q", reply1.PolicyAction)
	}

	reply2 := router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "emma-123", Text: "what about snapchat and social media",
	})
	if reply2.PolicyAction != "pending" {
		t.Errorf("second request should be pending, got %q", reply2.PolicyAction)
	}
}

// slowChat simulates a slow LLM — 200ms per response.
func slowChat(ctx context.Context, user *config.UserConfig, text string) (string, error) {
	time.Sleep(200 * time.Millisecond)
	return "slow: " + text, nil
}

// TestCrossUserConcurrency proves different users are processed in parallel.
// If serial: ~400ms. If concurrent: ~200ms.
func TestCrossUserConcurrency(t *testing.T) {
	router, identStore := setupRouter(t, slowChat)

	identStore.LinkAccount("parent", "telegram", "parent-123")
	identStore.LinkAccount("emma", "telegram", "emma-123")

	start := time.Now()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		router.Handle(context.Background(), Message{
			Gateway: "telegram", ExternalID: "parent-123", Text: "hello from parent",
		})
	}()
	go func() {
		defer wg.Done()
		router.Handle(context.Background(), Message{
			Gateway: "telegram", ExternalID: "emma-123", Text: "help with math",
		})
	}()

	wg.Wait()
	elapsed := time.Since(start)

	// If serial: ~400ms. If concurrent: ~200ms (+overhead).
	if elapsed > 350*time.Millisecond {
		t.Errorf("cross-user took %v — should be ~200ms (concurrent), not ~400ms (serial)", elapsed)
	}
}

// ── fix-109-110: gateway self-registration tests ────────────────────────────

// TestHandleUnknownAccount_AutoLinkExactName confirms that an exact (case-insensitive)
// match between Message.DisplayName and a configured FamClaw user's DisplayName
// auto-links the platform account to that user.
func TestHandleUnknownAccount_AutoLinkExactName(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	reply := router.Handle(context.Background(), Message{
		Gateway:     "telegram",
		ExternalID:  "tg-emma-123",
		Text:        "hello",
		DisplayName: "Emma",
	})
	if reply.PolicyAction != "onboarding" {
		t.Errorf("PolicyAction = %q, want onboarding", reply.PolicyAction)
	}

	user, err := identStore.Resolve(context.Background(), "telegram", "tg-emma-123")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if user == nil || user.Name != "emma" {
		t.Fatalf("expected emma to be auto-linked, got %v", user)
	}
}

// TestHandleUnknownAccount_AutoLinkFirstWord confirms that the first-word
// fallback fires: DisplayName "Emma Smith" matches user emma (DisplayName "Emma").
func TestHandleUnknownAccount_AutoLinkFirstWord(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	reply := router.Handle(context.Background(), Message{
		Gateway:     "telegram",
		ExternalID:  "tg-emma-456",
		Text:        "hi",
		DisplayName: "Emma Smith",
	})
	if reply.PolicyAction != "onboarding" {
		t.Errorf("PolicyAction = %q, want onboarding", reply.PolicyAction)
	}

	user, err := identStore.Resolve(context.Background(), "telegram", "tg-emma-456")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if user == nil || user.Name != "emma" {
		t.Fatalf("expected emma via first-word match, got %v", user)
	}
}

// TestHandleUnknownAccount_NumberedListWhenNoMatch confirms that a non-matching
// DisplayName produces the numbered-list prompt for disambiguation, including
// each unlinked user's DisplayName.
func TestHandleUnknownAccount_NumberedListWhenNoMatch(t *testing.T) {
	router, _ := setupRouter(t, echoChat)

	reply := router.Handle(context.Background(), Message{
		Gateway:     "discord",
		ExternalID:  "dc-stranger-1",
		Text:        "yo",
		DisplayName: "xXGamerXx",
	})
	if reply.PolicyAction != "onboarding" {
		t.Errorf("PolicyAction = %q, want onboarding", reply.PolicyAction)
	}
	// Parent role is deliberately excluded from gateway-side registration —
	// only children appear (security: prevents stranger-with-matching-name
	// takeover of parent accounts).
	for _, name := range []string{"Which family member", "Emma", "Lucas", "Sofia"} {
		if !strings.Contains(reply.Text, name) {
			t.Errorf("reply missing %q; got: %s", name, reply.Text)
		}
	}
	if strings.Contains(reply.Text, "Parent") {
		t.Errorf("parent must not appear in gateway numbered list; got: %s", reply.Text)
	}
}

// TestHandleUnknownAccount_RejectsWhenAllLinked confirms that with no
// unlinked users remaining, an unknown account gets the private-family
// rejection rather than a numbered list.
func TestHandleUnknownAccount_RejectsWhenAllLinked(t *testing.T) {
	router, identStore := setupRouter(t, echoChat)

	// Link every configured user to a distinct external ID so UnlinkedUsers
	// returns an empty slice for this gateway.
	links := []struct {
		userName, externalID string
	}{
		{"parent", "tg-parent-x"},
		{"emma", "tg-emma-x"},
		{"lucas", "tg-lucas-x"},
		{"sofia", "tg-sofia-x"},
	}
	for _, l := range links {
		if err := identStore.LinkAccount(l.userName, "telegram", l.externalID); err != nil {
			t.Fatalf("LinkAccount %s: %v", l.userName, err)
		}
	}

	reply := router.Handle(context.Background(), Message{
		Gateway:     "telegram",
		ExternalID:  "tg-stranger-9",
		Text:        "hello",
		DisplayName: "Stranger",
	})
	if !strings.Contains(reply.Text, "private family") {
		t.Errorf("expected private-family rejection, got: %s", reply.Text)
	}
	if reply.PolicyAction != "onboarding" {
		t.Errorf("PolicyAction = %q, want onboarding", reply.PolicyAction)
	}
}

// TestHandleRegistrationReply_ValidChoice runs the two-step flow:
// first a non-matching DisplayName creates a pendingRegistration with
// the numbered list (children only — parent is excluded for security),
// then a "1" reply links the first unlinked CHILD (emma in this fixture).
func TestHandleRegistrationReply_ValidChoice(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	// Step 1: trigger numbered-list pendingRegistration
	router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "tg-anon-1",
		Text: "yo", DisplayName: "Anonymous",
	})

	// Step 2: pick option 1 (emma — first non-parent in fixture order).
	reply := router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "tg-anon-1",
		Text: "1", DisplayName: "Anonymous",
	})
	if !strings.Contains(reply.Text, "Welcome") {
		t.Errorf("expected Welcome message, got: %s", reply.Text)
	}

	user, err := identStore.Resolve(context.Background(), "telegram", "tg-anon-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if user == nil || user.Name != "emma" {
		t.Fatalf("expected emma linked (parent excluded from gateway flow), got %v", user)
	}
}

// TestHandleRegistrationReply_InvalidInput confirms that non-numeric or
// out-of-range replies to the numbered-list prompt return the help text.
func TestHandleRegistrationReply_InvalidInput(t *testing.T) {
	router, _ := setupRouter(t, echoChat)

	router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "tg-anon-2",
		Text: "yo", DisplayName: "Anonymous",
	})
	reply := router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "tg-anon-2",
		Text: "foo", DisplayName: "Anonymous",
	})
	// Three children unlinked (parent excluded from the list), so the
	// help text quotes "between 1 and 3".
	if !strings.Contains(reply.Text, "number between 1 and 3") {
		t.Errorf("expected 'number between 1 and 3', got: %s", reply.Text)
	}
}

// TestHandleRegistrationReply_TypoKeepsPending verifies the CodeRabbit
// fix that an invalid reply does NOT delete the pendingRegistration —
// the user gets the help text and can try again with the same list.
func TestHandleRegistrationReply_TypoKeepsPending(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	// Step 1: trigger numbered list
	router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "tg-typo-1",
		Text: "yo", DisplayName: "Anonymous",
	})

	// Step 2: type a non-number — should get help, NOT drop the pending entry
	router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "tg-typo-1",
		Text: "I'm Emma", DisplayName: "Anonymous",
	})

	// Pending entry must still exist so the next reply can pick from
	// the same list rather than starting over.
	router.pendingMu.Lock()
	_, stillPending := router.pendingRegs["telegram:tg-typo-1"]
	router.pendingMu.Unlock()
	if !stillPending {
		t.Fatal("expected pendingRegistration to survive an invalid reply")
	}

	// Step 3: now reply with a valid number — should succeed
	router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "tg-typo-1",
		Text: "1", DisplayName: "Anonymous",
	})
	user, err := identStore.Resolve(context.Background(), "telegram", "tg-typo-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if user == nil || user.Name != "emma" {
		t.Fatalf("expected emma linked after valid retry, got %v", user)
	}

	// And the entry is now cleaned up (link succeeded).
	router.pendingMu.Lock()
	_, postLinkPending := router.pendingRegs["telegram:tg-typo-1"]
	router.pendingMu.Unlock()
	if postLinkPending {
		t.Error("expected pendingRegistration to be deleted after successful link")
	}
}

// TestHandleUnknownAccount_ParentNeverAutoLinked verifies the security fix
// from CodeRabbit thread on router.go:315 / identity/store.go:74. A stranger
// whose Telegram first name happens to equal the parent's family-side
// DisplayName must NOT auto-link to the parent account — they should be
// shown a list of children only (or rejected if no children remain).
func TestHandleUnknownAccount_ParentNeverAutoLinked(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	// Stranger with display name exactly matching the parent's DisplayName.
	reply := router.Handle(context.Background(), Message{
		Gateway:     "telegram",
		ExternalID:  "tg-impostor-1",
		Text:        "hi",
		DisplayName: "Parent",
	})
	// Must NOT be linked to the parent user.
	user, err := identStore.Resolve(context.Background(), "telegram", "tg-impostor-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if user != nil && user.Name == "parent" {
		t.Fatalf("SECURITY: stranger with DisplayName='Parent' was auto-linked to parent account")
	}
	// Should fall through to the numbered list (children only) since
	// "Parent" doesn't match any child's DisplayName.
	if !strings.Contains(reply.Text, "Which family member") {
		t.Errorf("expected numbered-list fallthrough, got: %s", reply.Text)
	}
}

// TestRouter_UnknownAccountFlows is table-driven and covers the unknown-account
// lifecycle through the router: record on first hit, clear on auto-link, clear
// on numbered-list link. Every case uses panicChat as the LLM, so any case that
// reaches the LLM would panic — that proves the policy gate (and the unknown
// path's pre-policy short-circuit) keeps the LLM unreachable for unknown
// accounts. The explicit "llm-must-not-be-called" case in the table is
// redundant by construction but documents the invariant.
func TestRouter_UnknownAccountFlows(t *testing.T) {
	type step struct {
		msg                Message
		wantAction         string
		wantTextContains   string
		wantUnknownCount   int
		wantUnknownGateway string
		wantUnknownExtID   string
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{
			name: "first unknown hit records row, numbered-list link clears it",
			steps: []step{
				{
					msg:                Message{Gateway: "telegram", ExternalID: "X1", Text: "yo", DisplayName: "Stranger"},
					wantAction:         "onboarding",
					wantUnknownCount:   1,
					wantUnknownGateway: "telegram",
					wantUnknownExtID:   "X1",
				},
				{
					msg:              Message{Gateway: "telegram", ExternalID: "X1", Text: "1", DisplayName: "Stranger"},
					wantAction:       "onboarding",
					wantUnknownCount: 0,
				},
			},
		},
		{
			name: "auto-link by display-name clears row in one shot",
			steps: []step{
				{
					msg:              Message{Gateway: "telegram", ExternalID: "tg-emma-auto", Text: "hi", DisplayName: "Emma"},
					wantAction:       "onboarding",
					wantTextContains: "linked",
					wantUnknownCount: 0,
				},
			},
		},
		{
			name: "llm-must-not-be-called when account is unknown (policy gate)",
			steps: []step{
				{
					// panicChat would panic if invoked. Reaching the LLM means the
					// policy gate (or the unknown short-circuit before it) failed.
					msg:              Message{Gateway: "telegram", ExternalID: "X-untouched", Text: "tell me anything", DisplayName: "Nobody"},
					wantAction:       "onboarding",
					wantUnknownCount: 1,
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router, identStore := setupRouter(t, panicChat)
			ctx := context.Background()

			for i, st := range tc.steps {
				reply := router.Handle(ctx, st.msg)
				if reply.PolicyAction != st.wantAction {
					t.Fatalf("step %d: PolicyAction = %q, want %q (text=%q)", i, reply.PolicyAction, st.wantAction, reply.Text)
				}
				if st.wantTextContains != "" && !strings.Contains(strings.ToLower(reply.Text), st.wantTextContains) {
					t.Errorf("step %d: reply text missing %q: %s", i, st.wantTextContains, reply.Text)
				}

				list, err := identStore.ListUnknown(ctx)
				if err != nil {
					t.Fatalf("step %d: ListUnknown: %v", i, err)
				}
				if len(list) != st.wantUnknownCount {
					t.Fatalf("step %d: unknown count = %d, want %d (rows: %+v)", i, len(list), st.wantUnknownCount, list)
				}
				if st.wantUnknownGateway != "" {
					if list[0].Gateway != st.wantUnknownGateway || list[0].ExternalID != st.wantUnknownExtID {
						t.Errorf("step %d: row mismatch: got %+v, want gateway=%s extID=%s",
							i, list[0], st.wantUnknownGateway, st.wantUnknownExtID)
					}
				}
			}
		})
	}
}

// TestCleanExpiredPending verifies that pendingRegistration entries
// older than 5 minutes are dropped on the next sweep.
func TestCleanExpiredPending(t *testing.T) {
	router, _ := setupRouter(t, echoChat)

	router.pendingMu.Lock()
	router.pendingRegs["telegram:expired-1"] = &pendingRegistration{
		gateway:    "telegram",
		externalID: "expired-1",
		askedAt:    time.Now().Add(-10 * time.Minute),
	}
	router.pendingRegs["telegram:fresh-1"] = &pendingRegistration{
		gateway:    "telegram",
		externalID: "fresh-1",
		askedAt:    time.Now(),
	}
	router.pendingMu.Unlock()

	router.cleanExpiredPending()

	router.pendingMu.Lock()
	defer router.pendingMu.Unlock()
	if _, ok := router.pendingRegs["telegram:expired-1"]; ok {
		t.Error("expired entry should have been deleted")
	}
	if _, ok := router.pendingRegs["telegram:fresh-1"]; !ok {
		t.Error("fresh entry should have been preserved")
	}
}

// TestCreateApprovalSkipsParentNotify verifies createApproval fires a
// notification for a child-triggered approval but skips it entirely for a
// parent-triggered one.
func TestCreateApprovalSkipsParentNotify(t *testing.T) {
	var notifyCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&notifyCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	router, _ := setupRouter(t, panicChat)
	router.notifier = notify.NewMultiNotifier(config.NotificationsConfig{
		Ntfy: config.NtfyConfig{Enabled: true, URL: srv.URL, Topic: "test"},
	}, "test-secret")

	child := &config.UserConfig{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"}
	router.createApproval(context.Background(), child, "violence", "why do wars happen", "req-child")
	if got := atomic.LoadInt32(&notifyCalls); got != 1 {
		t.Fatalf("child approval should notify once, got %d calls", got)
	}

	parent := &config.UserConfig{Name: "parent", DisplayName: "Parent", Role: "parent"}
	router.createApproval(context.Background(), parent, "violence", "why do wars happen", "req-parent")
	if got := atomic.LoadInt32(&notifyCalls); got != 1 {
		t.Fatalf("parent approval must not notify, but total calls rose to %d", got)
	}
}

// TestHandleSkillCommand verifies the parent-gated skill management commands.
func TestHandleSkillCommand(t *testing.T) {
	dbTmpDir := t.TempDir()
	db, err := store.Open(filepath.Join(dbTmpDir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:     "localhost",
			Port:     8080,
			Secret:   "test-secret",
			MDNSName: "famclaw",
		},
		LLM: config.LLMConfig{
			Temperature:       0.7,
			MaxResponseTokens: 512,
		},
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
			{Name: "child", DisplayName: "Child", Role: "child", AgeGroup: "age_8_12"},
		},
	}

	identStore := identity.NewStore(db)
	clf := classifier.New()
	notifier := notify.NewMultiNotifier(config.NotificationsConfig{}, "test-secret")
	skillTmpDir := t.TempDir()
	reg := skillbridge.NewRegistry(skillTmpDir, nil, skillbridge.InstallConfig{})
	chatFn := func(ctx context.Context, user *config.UserConfig, text string) (string, error) {
		return "stub", nil
	}
	router := NewRouter(context.Background(), cfg, identStore, clf, ev, db, notifier, chatFn, reg)

	// Link parent and child accounts
	identStore.LinkAccount("parent", "telegram", "parent-123")
	identStore.LinkAccount("child", "telegram", "child-123")

	// Pre-create a fake skill on disk so "list" is not empty
	skillDir := filepath.Join(skillTmpDir, "fakeskill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	skillMD := `---
name: fakeskill
description: A fake test skill
---
This is a fake skill.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name            string
		gateway         string
		externalID      string
		text            string
		wantPolicyAction string
		wantTextContain  string
	}{
		{
			name:            "child skill list blocked",
			gateway:         "telegram",
			externalID:      "child-123",
			text:            "skill list",
			wantPolicyAction: "block",
			wantTextContain:  "Only a parent",
		},
		{
			name:            "parent skill list",
			gateway:         "telegram",
			externalID:      "parent-123",
			text:            "skill list",
			wantPolicyAction: "skill",
			wantTextContain:  "fakeskill",
		},
		{
			name:            "parent skill no args",
			gateway:         "telegram",
			externalID:      "parent-123",
			text:            "skill",
			wantPolicyAction: "skill",
			wantTextContain:  "Skill management",
		},
		{
			name:            "parent skill unknown",
			gateway:         "telegram",
			externalID:      "parent-123",
			text:            "skill uninstall myskill",
			wantPolicyAction: "skill",
			wantTextContain:  "Unknown skill command",
		},
		{
			name:            "child skill install blocked",
			gateway:         "telegram",
			externalID:      "child-123",
			text:            "skill install myskill",
			wantPolicyAction: "block",
			wantTextContain:  "Only a parent",
		},
		{
			name:            "case insensitive skill prefix",
			gateway:         "telegram",
			externalID:      "parent-123",
			text:            "SKILL list",
			wantPolicyAction: "skill",
			wantTextContain:  "fakeskill",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply := router.Handle(context.Background(), Message{
				Gateway:    tt.gateway,
				ExternalID: tt.externalID,
				Text:       tt.text,
			})
			if reply.PolicyAction != tt.wantPolicyAction {
				t.Errorf("policy action = %q, want %q", reply.PolicyAction, tt.wantPolicyAction)
			}
			if !strings.Contains(reply.Text, tt.wantTextContain) {
				t.Errorf("text = %q, want to contain %q", reply.Text, tt.wantTextContain)
			}
		})
	}
}
