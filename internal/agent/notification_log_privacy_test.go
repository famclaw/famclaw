package agent

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

func TestSuspiciousNotificationFailureLogPhase(t *testing.T) {
	selected := os.Getenv("FAMCLAW_TEST_NOTIFY_PHASE")
	if selected == "" {
		for _, phase := range []string{"read", "send", "record"} {
			t.Run(phase, func(t *testing.T) {
				cmd := exec.Command(os.Args[0], "-test.run=^TestSuspiciousNotificationFailureLogPhase$")
				cmd.Env = append(os.Environ(), "FAMCLAW_TEST_NOTIFY_PHASE="+phase)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("child failed: %v\n%s", err, out)
				}
			})
		}
		return
	}
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	approval := &store.Approval{ID: "fake-id", UserName: "fake-user", UserDisplay: "fake-user", AgeGroup: "age_5_12", Category: "suspicious_site", QueryText: "fake-host"}
	if _, err := db.UpsertApproval(context.Background(), approval); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lifetime, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	notify := func(context.Context, *store.Approval) error {
		switch selected {
		case "send":
			return errors.New("fake-error https://example.com/fake-path?token=fake-token")
		case "record":
			shutdown()
		}
		return nil
	}
	a := &Agent{user: &config.UserConfig{Name: "fake-user", Role: "child"}, db: db, lifetimeCtx: lifetime, onNewSuspiciousApproval: notify}
	if selected == "read" {
		cancel()
	}
	var out bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(prev)
	a.notifySuspiciousApprovalIfNeeded(ctx, "fake-id", approval)
	text := out.String()
	if !strings.Contains(text, selected) {
		t.Fatalf("missing phase %q in warning: %q", selected, text)
	}
	for _, secret := range []string{"fake-user", "fake-id", "fake-error", "fake-path", "fake-token", "example.com"} {
		if strings.Contains(text, secret) {
			t.Fatalf("warning leaked %q", secret)
		}
	}
}
