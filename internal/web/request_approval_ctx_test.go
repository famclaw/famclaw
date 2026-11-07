package web

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/agent"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// TestRequestApproval_HonorsContext locks in the #423 sovereign-codereviewer
// fix for requestApproval using context.Background(). The function must
// thread the caller's context into the approval upsert (and the parent
// notification) so that both abort when the context is cancelled — e.g. on
// WebSocket connection close or server shutdown — instead of running
// fire-and-forget against a store that may already be torn down.
//
// A context cancelled before the call must therefore leave NO approval row
// in the DB. The unfixed code (context.Background()) ignores the
// cancellation and persists the row, so this test is RED on the unfixed code
// and GREEN once the ctx is threaded through.
func TestRequestApproval_HonorsContext(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := store.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"},
		},
	}
	// No notifier is wired: the only observable side effect is the DB upsert.
	s := &Server{cfg: cfg, db: db, cfgMu: sync.RWMutex{}}

	user := &cfg.Users[0]
	category := "violence"
	approvalID := agent.ApprovalID(user.Name, category)

	// Pre-cancelled context: the upsert must observe it and abort.
	ctl, cancel := context.WithCancel(context.Background())
	cancel()

	// In production requestApproval runs in a spawnBackground goroutine;
	// call it directly here so the assertion is deterministic.
	s.requestApproval(ctl, user, category, "why is the sky blue")

	// Read back with a live context (the test ctx is already cancelled).
	got, gerr := db.GetApproval(context.Background(), approvalID)
	if gerr != nil {
		t.Fatalf("GetApproval: %v", gerr)
	}
	if got != nil {
		t.Fatalf("requestApproval persisted an approval row (status=%q) despite a cancelled context; it must honor the caller's context, not context.Background()", got.Status)
	}
}
