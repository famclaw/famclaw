package web

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
)

func TestApprovalSkippedOnShutdownWarns(t *testing.T) {
	if os.Getenv("FAMCLAW_TEST_APPROVAL_SHUTDOWN") == "1" {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s := &Server{lifetimeCtx: ctx}
		s.enqueueApproval(&config.UserConfig{Name: "fake-user"}, "fake-category", "fake-query")
		if err := s.WaitForBackground(context.Background()); err != nil {
			t.Fatal(err)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestApprovalSkippedOnShutdownWarns$")
	cmd.Env = append(os.Environ(), "FAMCLAW_TEST_APPROVAL_SHUTDOWN=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child test: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "approval skipped") || !strings.Contains(text, "shutdown") {
		t.Fatalf("missing shutdown warning: %q", text)
	}
	for _, secret := range []string{"fake-user", "fake-category", "fake-query"} {
		if strings.Contains(text, secret) {
			t.Fatalf("warning exposed %q", secret)
		}
	}
}
