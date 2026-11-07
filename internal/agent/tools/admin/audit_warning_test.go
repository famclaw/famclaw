package admin

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMissingAuditStoreWarning(t *testing.T) {
	if os.Getenv("FAMCLAW_TEST_MISSING_AUDIT") == "1" {
		if err := logAudit(context.Background(), Deps{Actor: "private-actor", Gateway: "private-gateway"}, "private-tool", map[string]any{"secret": "private-payload"}); err != nil {
			t.Fatal(err)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMissingAuditStoreWarning$")
	cmd.Env = append(os.Environ(), "FAMCLAW_TEST_MISSING_AUDIT=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child test: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "audit") || !strings.Contains(text, "unavailable") {
		t.Fatalf("missing audit warning: %q", text)
	}
	for _, secret := range []string{"private-actor", "private-gateway", "private-tool", "private-payload"} {
		if strings.Contains(text, secret) {
			t.Fatalf("warning leaked private value: %q", secret)
		}
	}
}
