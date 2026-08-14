package gateway

import (
	"context"
	"fmt"
	"log"
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
func panicChat(ctx context.Context, user *config.UserConfig, text string, msgCtx MsgContext) (string, error) {
	panic("LLM called when it should not have been — policy gate FAILED")
}

// echoChat returns a predictable response for testing the allow path.
func echoChat(ctx context.Context, user *config.UserConfig, text string, msgCtx MsgContext) (string, error) {
	return "echo: " + text, nil
}

// errorChat simulates an LLM error.
func errorChat(ctx context.Context, user *config.UserConfig, text string, msgCtx MsgContext) (string, error) {
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
	notifier := notify.NewMultiNotifier(cfg, identStore, func(ctx context.Context, gw, chatID, text string) error { return nil })
	reg := skillbridge.NewRegistry(t.TempDir(), nil, skillbridge.InstallConfig{}, nil)

	router := NewRouter(context.Background(), cfg, identStore, clf, ev, db, notifier, chatFn, reg, "", nil)
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

// TestRouterIdentityResolutionError verifies that when identity resolution
// fails (e.g. the backing DB is unavailable), both Handle and process
// return a Reply with PolicyAction "error" rather than silently discarding
// the error.
//
// An error-handling audit flagged these two sites: Handle was said to log
// the error but send a generic reply "hiding the failure", and process
// was said to discard its re-resolution error. Both sites already log the
// error and return Reply{PolicyAction:"error"}; this test locks that
// behavior in so it cannot silently regress.
//
// The test closes the router's DB to force Resolve to error (a closed
// *sql.DB makes QueryRowContext return an error rather than sql.ErrNoRows,
// so identity.Store.Resolve surfaces a non-nil error), then asserts both
// entry points return an error reply and never reach the LLM (panicChat
// would panic if invoked).
func TestRouterIdentityResolutionError(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	// Link a user so the test exercises a *known* identity whose resolution
	// fails — proving the error isn't mistaken for an unknown account
	// (which would return onboarding instead of error).
	if err := identStore.LinkAccount("emma", "telegram", "err-emma-123"); err != nil {
		t.Fatalf("LinkAccount: %v", err)
	}

	// Close the DB so the next Resolve call errors out.
	if err := router.db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	ctx := context.Background()
	msg := Message{Gateway: "telegram", ExternalID: "err-emma-123", Text: "hello"}

	cases := []struct {
		name       string
		call       func(context.Context, Message) Reply
		wantAction string
	}{
		{
			name:       "Handle",
			call:       router.Handle,
			wantAction: "error",
		},
		{
			name:       "process",
			call:       router.process,
			wantAction: "error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := tc.call(ctx, msg)
			if reply.PolicyAction != tc.wantAction {
				t.Errorf("PolicyAction = %q, want %q — identity error must not be silently discarded",
					reply.PolicyAction, tc.wantAction)
			}
			if !strings.Contains(reply.Text, "Something went wrong") {
				t.Errorf("reply text = %q, want it to contain 'Something went wrong'", reply.Text)
			}
		})
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

	stop := StartAll(ctx, []Gateway{gw}, func(ctx context.Context, msg Message) Reply {
		return Reply{Text: "ok"}
	})

	// Wait for gateway to start
	name := <-started
	if name != "mock" {
		t.Errorf("expected mock, got %q", name)
	}
	cancel()
	// stop() blocks until the goroutine exits — proves no leak.
	stop()
}

// TestStartAllStopsOnCancel proves that the goroutines launched by StartAll
// exit promptly when the context is cancelled — no leak, no hang. It
// exercises multiple gateways to confirm every goroutine drains.
func TestStartAllStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	const n = 5
	started := make(chan struct{}, n)

	gws := make([]Gateway, 0, n)
	for i := 0; i < n; i++ {
		gws = append(gws, &mockGateway{
			name: fmt.Sprintf("gw-%d", i),
			startFn: func(ctx context.Context, h func(context.Context, Message) Reply) error {
				started <- struct{}{}
				<-ctx.Done()
				return ctx.Err()
			},
		})
	}

	stop := StartAll(ctx, gws, func(context.Context, Message) Reply {
		return Reply{Text: "ok"}
	})

	// Wait for all gateways to signal they are running.
	for i := 0; i < n; i++ {
		<-started
	}

	// Signal shutdown.
	cancel()

	// The stop function should return promptly — every goroutine exits
	// on context cancellation.
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
		// All gateway goroutines exited cleanly — no leak.
	case <-time.After(2 * time.Second):
		t.Fatal("gateway goroutines did not stop within 2s after context cancellation")
	}
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
func slowChat(ctx context.Context, user *config.UserConfig, text string, msgCtx MsgContext) (string, error) {
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
	router, identStore := setupRouter(t, panicChat)

	// Link the parent to a gateway account so the notifier can deliver.
	if err := identStore.LinkAccount("parent", "telegram", "parent-tg"); err != nil {
		t.Fatalf("link account: %v", err)
	}

	var notifyCalls int32
	router.notifier = notify.NewMultiNotifier(router.cfg, identStore,
		func(ctx context.Context, gw, chatID, text string) error {
			atomic.AddInt32(&notifyCalls, 1)
			return nil
		})

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
	notifier := notify.NewMultiNotifier(cfg, identStore, func(ctx context.Context, gw, chatID, text string) error { return nil })
	skillTmpDir := t.TempDir()
	reg := skillbridge.NewRegistry(skillTmpDir, nil, skillbridge.InstallConfig{}, nil)
	chatFn := func(ctx context.Context, user *config.UserConfig, text string, msgCtx MsgContext) (string, error) {
		return "stub", nil
	}
	router := NewRouter(context.Background(), cfg, identStore, clf, ev, db, notifier, chatFn, reg, "", nil)

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
		name             string
		gateway          string
		externalID       string
		text             string
		wantPolicyAction string
		wantTextContain  string
	}{
		{
			name:             "child skill list blocked",
			gateway:          "telegram",
			externalID:       "child-123",
			text:             "skill list",
			wantPolicyAction: "block",
			wantTextContain:  "Only a parent",
		},
		{
			name:             "parent skill list",
			gateway:          "telegram",
			externalID:       "parent-123",
			text:             "skill list",
			wantPolicyAction: "skill",
			wantTextContain:  "fakeskill",
		},
		{
			name:             "parent skill no args",
			gateway:          "telegram",
			externalID:       "parent-123",
			text:             "skill",
			wantPolicyAction: "skill",
			wantTextContain:  "Skill management",
		},
		{
			name:             "parent skill unknown",
			gateway:          "telegram",
			externalID:       "parent-123",
			text:             "skill uninstall myskill",
			wantPolicyAction: "skill",
			wantTextContain:  "Unknown skill command",
		},
		{
			name:             "child skill install blocked",
			gateway:          "telegram",
			externalID:       "child-123",
			text:             "skill install myskill",
			wantPolicyAction: "block",
			wantTextContain:  "Only a parent",
		},
		{
			name:             "case insensitive skill prefix",
			gateway:          "telegram",
			externalID:       "parent-123",
			text:             "SKILL list",
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

// TestRouterShutdown verifies that calling router.Shutdown() cancels the
// session pool context so in-flight goroutines can exit cleanly.
func TestRouterShutdown(t *testing.T) {
	router, identStore := setupRouter(t, echoChat)
	identStore.LinkAccount("parent", "telegram", "parent-123")

	// Send a message to start a session goroutine
	done := make(chan Reply, 1)
	go func() {
		done <- router.Handle(context.Background(), Message{
			Gateway: "telegram", ExternalID: "parent-123", Text: "hello",
		})
	}()

	// Shut down immediately — the session context should be cancelled.
	time.Sleep(50 * time.Millisecond)
	router.Shutdown()

	select {
	case <-done:
		// Got a reply (either before or after shutdown), that's fine.
	case <-time.After(2 * time.Second):
		t.Fatal("router.Handle goroutine did not exit after Shutdown()")
	}
}

// TestRunGatewayPanicRecovery verifies that a gateway Start function that
// panics is recovered gracefully (log message printed, no process crash).
func TestRunGatewayPanicRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	panicGw := &mockGateway{
		name: "panic-gw",
		startFn: func(ctx context.Context, h func(context.Context, Message) Reply) error {
			panic("BOOM — simulated crash")
		},
	}

	// runGateway should recover from the panic and keep retrying.
	// We start it, let it panic once, then cancel the context.
	gotPanic := make(chan struct{}, 1)
	log.SetOutput(&panicLogger{fn: func(p string) {
		if strings.Contains(p, "PANIC") {
			select {
			case gotPanic <- struct{}{}:
			default:
			}
		}
	}})
	defer log.SetOutput(os.Stderr)

	stop := StartAll(ctx, []Gateway{panicGw}, func(ctx context.Context, msg Message) Reply {
		return Reply{Text: "ok"}
	})

	select {
	case <-gotPanic:
		// Recovery log was printed — good.
	case <-time.After(2 * time.Second):
		t.Fatal("expected PANIC recovery log but timed out")
	}
	cancel()

	// Wait for the gateway goroutine to exit cleanly.
	stop()
}

// panicLogger writes to a callback function.
type panicLogger struct {
	fn func(string)
}

func (l *panicLogger) Write(p []byte) (n int, err error) {
	l.fn(string(p))
	return len(p), nil
}

func (l *panicLogger) Read(p []byte) (n int, err error) {
	return 0, fmt.Errorf("not implemented")
}

func (l *panicLogger) Seek(offset int64, whence int) (int64, error) {
	return 0, fmt.Errorf("not implemented")
}

func (l *panicLogger) Stat() (os.FileInfo, error) {
	return nil, fmt.Errorf("not implemented")
}

// TestRunGatewayContextCancel verifies that runGateway exits cleanly when
// its context is cancelled during the backoff sleep.
func TestRunGatewayContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startFnCh := make(chan struct{})
	gwDone := make(chan struct{})
	gw := &mockGateway{
		name: "cancel-gw",
		startFn: func(ctx context.Context, h func(context.Context, Message) Reply) error {
			close(startFnCh) // signal that we are about to wait for ctx.Done
			<-ctx.Done()
			close(gwDone)
			return ctx.Err()
		},
	}

	// Run the gateway in a goroutine
	done := make(chan struct{})
	go func() {
		runGateway(ctx, gw, func(context.Context, Message) Reply {
			return Reply{Text: "ok"}
		})
		close(done)
	}()

	// Wait for the startFn to be about to wait for ctx.Done()
	<-startFnCh

	// Now cancel the context
	cancel()

	// Then wait for gwDone
	select {
	case <-gwDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("gateway goroutine did not exit after context cancellation")
	}
	<-done // wait for the runGateway goroutine to finish
}

// TestHandleSkillCommandInstallEnableDisable tests the mutating skill commands
// through the full Handle → process → handleSkillCommand pipeline.
func TestHandleSkillCommandInstallEnableDisable(t *testing.T) {
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
		},
	}

	identStore := identity.NewStore(db)
	clf := classifier.New()
	notifier := notify.NewMultiNotifier(cfg, identStore, func(ctx context.Context, gw, chatID, text string) error { return nil })
	skillTmpDir := t.TempDir()
	reg := skillbridge.NewRegistry(skillTmpDir, nil, skillbridge.InstallConfig{}, nil)
	chatFn := func(ctx context.Context, user *config.UserConfig, text string, msgCtx MsgContext) (string, error) {
		return "stub", nil
	}
	router := NewRouter(context.Background(), cfg, identStore, clf, ev, db, notifier, chatFn, reg, "", nil)
	identStore.LinkAccount("parent", "telegram", "parent-123")

	// 1. Install a skill from a pre-placed SKILL.md file.
	srcDir := t.TempDir()
	srcSkillDir := filepath.Join(srcDir, "testskill")
	if err := os.MkdirAll(srcSkillDir, 0755); err != nil {
		t.Fatal(err)
	}
	skillMD := `---
name: testskill
description: A test installable skill
---
Test skill content.
`
	if err := os.WriteFile(filepath.Join(srcSkillDir, "SKILL.md"), []byte(skillMD), 0644); err != nil {
		t.Fatal(err)
	}

	reply := router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "parent-123",
		Text: "skill install " + srcSkillDir,
	})
	if reply.PolicyAction != "skill" {
		t.Fatalf("install action = %q, want skill", reply.PolicyAction)
	}
	if !strings.Contains(reply.Text, "Installed skill") {
		t.Errorf("install reply = %q, want 'Installed skill'", reply.Text)
	}

	// 2. List should now include the installed skill.
	reply = router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "parent-123",
		Text: "skill list",
	})
	if !strings.Contains(reply.Text, "testskill") {
		t.Errorf("list after install missing testskill: %s", reply.Text)
	}

	// 3. Disable the skill.
	reply = router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "parent-123",
		Text: "skill disable testskill",
	})
	if !strings.Contains(reply.Text, "Disabled skill") {
		t.Errorf("disable reply = %q, want 'Disabled skill'", reply.Text)
	}
	if reg.IsEnabled("testskill") {
		t.Error("expected testskill to be disabled")
	}

	// 4. Enable the skill.
	reply = router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "parent-123",
		Text: "skill enable testskill",
	})
	if !strings.Contains(reply.Text, "Enabled skill") {
		t.Errorf("enable reply = %q, want 'Enabled skill'", reply.Text)
	}
	if !reg.IsEnabled("testskill") {
		t.Error("expected testskill to be enabled after enable command")
	}

	// 5. Install with no name — should return usage hint.
	reply = router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "parent-123",
		Text: "skill install",
	})
	if reply.PolicyAction != "skill" {
		t.Fatalf("install-no-args action = %q, want skill", reply.PolicyAction)
	}
	if !strings.Contains(reply.Text, "Usage") {
		t.Errorf("install-no-args text = %q, want 'Usage'", reply.Text)
	}

	// 6. Enable a non-existent skill should fail.
	reply = router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "parent-123",
		Text: "skill enable nonexistent",
	})
	if reply.PolicyAction != "error" {
		t.Fatalf("enable-nonexistent action = %q, want error", reply.PolicyAction)
	}
	if !strings.Contains(reply.Text, "Skill command failed") {
		t.Errorf("enable-nonexistent text = %q, want to contain 'Skill command failed'", reply.Text)
	}
}

// TestHandleSkillCommandEmptyList tests the skill list path when no skills exist.
func TestHandleSkillCommandEmptyList(t *testing.T) {
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
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
		},
	}

	identStore := identity.NewStore(db)
	clf := classifier.New()
	notifier := notify.NewMultiNotifier(cfg, identStore, func(ctx context.Context, gw, chatID, text string) error { return nil })
	skillTmpDir := t.TempDir()
	reg := skillbridge.NewRegistry(skillTmpDir, nil, skillbridge.InstallConfig{}, nil)
	chatFn := func(ctx context.Context, user *config.UserConfig, text string, msgCtx MsgContext) (string, error) {
		return "stub", nil
	}
	router := NewRouter(context.Background(), cfg, identStore, clf, ev, db, notifier, chatFn, reg, "", nil)
	identStore.LinkAccount("parent", "telegram", "parent-123")

	reply := router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "parent-123",
		Text: "skill list",
	})
	if reply.PolicyAction != "skill" {
		t.Fatalf("action = %q, want skill", reply.PolicyAction)
	}
	if !strings.Contains(reply.Text, "No skills installed") {
		t.Errorf("empty list text = %q, want 'No skills installed'", reply.Text)
	}
}

// TestRouterImageAttachmentForwarded proves that an image attachment
// present in the inbound Message reaches the chatFn (and thus the LLM)
// via MsgContext.Attachments — the single choke point that previously
// dropped attachments at router.go:232. A text-only message must be
// unchanged (no attachments, text forwarded verbatim).
//
// Uses neutral synthetic content — no real user data.
func TestRouterImageAttachmentForwarded(t *testing.T) {
	// A chatFn that captures what the router forwards.
	type captured struct {
		text        string
		attachments []Attachment
	}

	// Case 1: image attachment present — must arrive at chatFn.
	var got captured
	imgChatFn := func(ctx context.Context, user *config.UserConfig, text string, msgCtx MsgContext) (string, error) {
		got.text = text
		got.attachments = msgCtx.Attachments
		return "ok", nil
	}
	router, identStore := setupRouter(t, imgChatFn)
	identStore.LinkAccount("parent", "telegram", "parent-img")

	img := Attachment{
		Type:     "image",
		Data:     "iVBtb2NrLWJhc2U2NA", // neutral synthetic base64 (not real image bytes)
		MIMEType: "image/png",
	}

	reply := router.Handle(context.Background(), Message{
		Gateway:     "telegram",
		ExternalID:  "parent-img",
		Text:        "what is in this picture",
		Attachments: []Attachment{img},
	})
	if reply.PolicyAction != "allow" {
		t.Fatalf("parent image message should be allowed, got %q: %s", reply.PolicyAction, reply.Text)
	}
	if got.text != "what is in this picture" {
		t.Errorf("text = %q, want %q", got.text, "what is in this picture")
	}
	if len(got.attachments) != 1 {
		t.Fatalf("expected 1 attachment forwarded, got %d", len(got.attachments))
	}
	if got.attachments[0] != img {
		t.Errorf("attachment mismatch: got %+v, want %+v", got.attachments[0], img)
	}

	// Case 2: text-only message — no attachments, text unchanged.
	var got2 captured
	textChatFn := func(ctx context.Context, user *config.UserConfig, text string, msgCtx MsgContext) (string, error) {
		got2.text = text
		got2.attachments = msgCtx.Attachments
		return "ok", nil
	}
	router2, identStore2 := setupRouter(t, textChatFn)
	identStore2.LinkAccount("emma", "discord", "emma-txt")

	reply2 := router2.Handle(context.Background(), Message{
		Gateway:    "discord",
		ExternalID: "emma-txt",
		Text:       "hello world",
	})
	if reply2.PolicyAction != "allow" {
		t.Fatalf("child safe topic should be allowed, got %q", reply2.PolicyAction)
	}
	if got2.text != "hello world" {
		t.Errorf("text = %q, want %q", got2.text, "hello world")
	}
	if len(got2.attachments) != 0 {
		t.Errorf("text-only message should have 0 attachments, got %d", len(got2.attachments))
	}
}

// TestRouterVoiceAttachmentNeverSilent proves that a voice message — an audio
// attachment with NO text (the exact shape Telegram/Discord voice notes arrive
// in) — is never silently dropped. The router must forward the attachment to the
// chatFn and return a non-empty Reply (an allow / transcription path or a
// visible refusal), in every case.
//
// This is the router-level half of the guarantee that the agent-level tests in
// internal/agent/voice_transcribe_test.go prove in isolation. Together they
// close issue #310: "voice messages are silently dropped — no reply, no error."
//
// The chatFn here is a stand-in for agent.Chat which, in production, transcribes
// the audio attachment into text before calling the LLM. We use a mock that
// returns a transcript-like string so we can assert the full forward-and-reply
// path end to end.
func TestRouterVoiceAttachmentNeverSilent(t *testing.T) {
	type captured struct {
		text        string
		attachments []Attachment
		called      bool
	}

	cases := []struct {
		name        string
		userName    string
		externalID  string
		text        string
		attachments []Attachment
		wantAction  string
	}{
		{
			name:       "parent voice note no text",
			userName:   "parent",
			externalID: "parent-voice",
			text:       "",
			attachments: []Attachment{{
				Type:     "audio",
				Data:     "ZmFrZS1vZ2ctYnl0ZXM=", // base64 of "fake-ogg-bytes"
				MIMEType: "audio/ogg",
			}},
			wantAction: "allow",
		},
		{
			name:       "child voice note no text",
			userName:   "emma",
			externalID: "emma-voice",
			text:       "",
			attachments: []Attachment{{
				Type:     "audio",
				Data:     "ZmFrZS1vZ2ctYnl0ZXM=",
				MIMEType: "audio/ogg",
			}},
			wantAction: "allow",
		},
		{
			name:       "child voice note with caption text",
			userName:   "lucas",
			externalID: "lucas-voice",
			text:       "what is my homework",
			attachments: []Attachment{{
				Type:     "audio",
				Data:     "ZmFrZS1vZ2ctYnl0ZXM=",
				MIMEType: "audio/ogg",
			}},
			wantAction: "allow",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got captured
			chatFn := func(ctx context.Context, user *config.UserConfig, text string, msgCtx MsgContext) (string, error) {
				got.text = text
				got.attachments = msgCtx.Attachments
				got.called = true
				return "transcript: what time is it", nil
			}
			router, identStore := setupRouter(t, chatFn)
			identStore.LinkAccount(tc.userName, "telegram", tc.externalID)

			reply := router.Handle(context.Background(), Message{
				Gateway:     "telegram",
				ExternalID:  tc.externalID,
				Text:        tc.text,
				Attachments: tc.attachments,
			})

			// The chatFn MUST have been reached — proving the voice message
			// was not silently dropped before reaching the agent.
			if !got.called {
				t.Fatal("chatFn was never called — voice message was silently dropped")
			}

			// The chatFn must have received the audio attachment.
			if len(got.attachments) != 1 {
				t.Fatalf("expected 1 attachment forwarded to chatFn, got %d", len(got.attachments))
			}
			att := got.attachments[0]
			if att.Type != "audio" {
				t.Errorf("attachment type = %q, want audio", att.Type)
			}
			if att.MIMEType != "audio/ogg" {
				t.Errorf("attachment mime = %q, want audio/ogg", att.MIMEType)
			}

			// The text passed to chatFn must be the original text (empty for
			// voice notes), so the agent's transcribeAttachments can detect
			// that it needs to transcribe the audio.
			if got.text != tc.text {
				t.Errorf("text forwarded to chatFn = %q, want %q", got.text, tc.text)
			}

			// The router must return a non-empty reply — never silence.
			if reply.PolicyAction != tc.wantAction {
				t.Errorf("PolicyAction = %q, want %q", reply.PolicyAction, tc.wantAction)
			}
			if strings.TrimSpace(reply.Text) == "" {
				t.Error("reply text is empty — voice message produced silence")
			}
		})
	}
}

// TestRouterSavesGatewayOnMessage verifies that the real gateway name from the
// inbound Message is persisted by the router's SaveMessage calls — proving the
// gateway value originates from the gateway layer, not a store-layer default.
// Uses the block path (router calls SaveMessage directly) for determinism
// without depending on the LLM agent.
func TestRouterSavesGatewayOnMessage(t *testing.T) {
	cases := []struct {
		name       string
		gateway    string
		externalID string
	}{
		{name: "telegram", gateway: "telegram", externalID: "sofia-tg"},
		{name: "discord", gateway: "discord", externalID: "sofia-dc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router, identStore := setupRouter(t, panicChat)
			identStore.LinkAccount("sofia", tc.gateway, tc.externalID)

			reply := router.Handle(context.Background(), Message{
				Gateway:    tc.gateway,
				ExternalID: tc.externalID,
				Text:       "show me porn",
			})
			if reply.PolicyAction != "block" {
				t.Fatalf("critical category should be blocked, got %q", reply.PolicyAction)
			}

			// The most-recent-gateway query must reflect the real gateway.
			gw, err := router.db.MostRecentGatewayForUser("sofia")
			if err != nil {
				t.Fatalf("MostRecentGatewayForUser: %v", err)
			}
			if gw != tc.gateway {
				t.Errorf("most recent gateway = %q, want %q", gw, tc.gateway)
			}

			// Also verify via GetConversationHistory that messages read back
			// with the correct gateway.
			msgs, err := router.db.RecentMessagesByUser("sofia", 20)
			if err != nil {
				t.Fatalf("GetConversationHistory: %v", err)
			}
			if len(msgs) == 0 {
				t.Fatalf("expected saved messages, got 0")
			}
			for _, m := range msgs {
				if m.Gateway != tc.gateway {
					t.Errorf("message gateway = %q, want %q", m.Gateway, tc.gateway)
				}
			}
		})
	}
}
