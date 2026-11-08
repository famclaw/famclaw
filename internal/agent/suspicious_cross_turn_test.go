package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

func TestSuspiciousNotificationAcrossAgents(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failFirst bool
		renew     bool
		want      int32
	}{
		{name: "success once", want: 1},
		{name: "retry failure", failFirst: true, want: 2},
		{name: "renew expired approval", renew: true, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			cfg := &config.Config{Tools: config.ToolsConfig{WebFetch: config.WebFetchConfig{Enabled: true, URLPolicy: "denylist"}}}
			var calls atomic.Int32
			notify := func(context.Context, *store.Approval) error {
				n := calls.Add(1)
				if tc.failFirst && n == 1 {
					return errors.New("transient")
				}
				return nil
			}
			makeAgent := func() *Agent {
				return &Agent{user: &config.UserConfig{Name: "teo", Role: "child", AgeGroup: "age_5_12"}, cfg: cfg, db: db, onNewSuspiciousApproval: notify}
			}
			first, second := makeAgent(), makeAgent()
			if ok, _ := first.hostDecision(context.Background(), "googl.com"); ok {
				t.Fatal("suspicious host allowed")
			}
			if tc.renew {
				id := proposalApprovalID("teo", "suspicious_site", "googl.com")
				if err := db.DecideApproval(id, "approved", "parent"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.SQL().Exec("UPDATE approvals SET expires_at=? WHERE id=?", time.Now().Add(-time.Hour), id); err != nil {
					t.Fatal(err)
				}
			}
			if ok, _ := second.hostDecision(context.Background(), "googl.com"); ok {
				t.Fatal("suspicious host allowed")
			}
			if got := calls.Load(); got != tc.want {
				t.Fatalf("notification calls=%d want=%d", got, tc.want)
			}
		})
	}
}

func TestSuspiciousNotificationConcurrentAgents(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := &config.Config{Tools: config.ToolsConfig{WebFetch: config.WebFetchConfig{Enabled: true, URLPolicy: "denylist"}}}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var calls atomic.Int32
	notify := func(ctx context.Context, _ *store.Approval) error {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	makeAgent := func() *Agent {
		return &Agent{user: &config.UserConfig{Name: "teo", Role: "child", AgeGroup: "age_5_12"}, cfg: cfg, db: db, onNewSuspiciousApproval: notify}
	}
	first, second := makeAgent(), makeAgent()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { defer close(done); first.hostDecision(ctx, "googl.com") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("notification cleanup timed out")
		}
	})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first notification did not start")
	}
	second.hostDecision(ctx, "googl.com")
	close(release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("first notification did not finish")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent notifications=%d want=1", got)
	}
}

// An old in-flight notification must not suppress or mark a renewed request.
func TestSuspiciousNotificationRenewedWhileInFlight(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := &config.Config{Tools: config.ToolsConfig{WebFetch: config.WebFetchConfig{Enabled: true, URLPolicy: "denylist"}}}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var calls atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	notify := func(ctx context.Context, _ *store.Approval) error {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	makeAgent := func() *Agent {
		return &Agent{user: &config.UserConfig{Name: "teo", Role: "child", AgeGroup: "age_5_12"}, cfg: cfg, db: db, onNewSuspiciousApproval: notify}
	}
	a, b := makeAgent(), makeAgent()
	go func() { defer close(done); a.hostDecision(ctx, "googl.com") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("notification cleanup timed out")
		}
	})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("old notification did not start")
	}
	id := proposalApprovalID("teo", "suspicious_site", "googl.com")
	if err := db.DecideApproval(id, "approved", "parent"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec("UPDATE approvals SET expires_at=? WHERE id=?", time.Now().Add(-time.Hour), id); err != nil {
		t.Fatal(err)
	}
	b.hostDecision(ctx, "googl.com")
	close(release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("old notification did not stop")
	}
	b.hostDecision(ctx, "googl.com")
	if got := calls.Load(); got != 2 {
		t.Fatalf("notifications=%d want one per approval generation", got)
	}
}

func TestSuspiciousNotificationRecordedAfterTurnCancellation(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := &config.Config{Tools: config.ToolsConfig{WebFetch: config.WebFetchConfig{Enabled: true, URLPolicy: "denylist"}}}
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	turn, cancel := context.WithCancel(lifetime)
	defer cancel()
	var calls atomic.Int32
	notify := func(context.Context, *store.Approval) error { calls.Add(1); cancel(); return nil }
	makeAgent := func() *Agent {
		return &Agent{user: &config.UserConfig{Name: "teo", Role: "child", AgeGroup: "age_5_12"}, cfg: cfg, db: db, lifetimeCtx: lifetime, onNewSuspiciousApproval: notify}
	}
	makeAgent().hostDecision(turn, "googl.com")
	if turn.Err() != context.Canceled {
		t.Fatal("turn was not canceled after successful notification")
	}
	makeAgent().hostDecision(lifetime, "googl.com")
	if got := calls.Load(); got != 1 {
		t.Fatalf("successful notification resent after turn cancellation: calls=%d", got)
	}
}

func TestSuspiciousNotificationLifetimeOutlivesTurn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		shutdown bool
	}{{"turn canceled", false}, {"server shutdown", true}} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			cfg := &config.Config{Tools: config.ToolsConfig{WebFetch: config.WebFetchConfig{Enabled: true, URLPolicy: "denylist"}}}
			lifetime, shutdown := context.WithCancel(context.Background())
			turn, cancelTurn := context.WithCancel(lifetime)
			entered := make(chan context.Context, 1)
			release := make(chan struct{})
			done := make(chan struct{})
			var once sync.Once
			var calls atomic.Int32
			notify := func(ctx context.Context, _ *store.Approval) error {
				calls.Add(1)
				entered <- ctx
				<-release
				return ctx.Err()
			}
			makeAgent := func() *Agent {
				return &Agent{user: &config.UserConfig{Name: "teo", Role: "child", AgeGroup: "age_5_12"}, cfg: cfg, db: db, lifetimeCtx: lifetime, onNewSuspiciousApproval: notify}
			}
			a := makeAgent()
			go func() { defer close(done); a.hostDecision(turn, "googl.com") }()
			t.Cleanup(func() {
				cancelTurn()
				shutdown()
				once.Do(func() { close(release) })
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("notification did not stop")
				}
			})
			var notifyCtx context.Context
			select {
			case notifyCtx = <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("notification did not start")
			}
			deadline, ok := notifyCtx.Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second {
				t.Fatal("notification operation does not have its bounded deadline")
			}
			if tc.shutdown {
				shutdown()
			} else {
				cancelTurn()
			}
			if got := notifyCtx.Err(); tc.shutdown && got != context.Canceled {
				t.Fatalf("notification context=%v want canceled", got)
			} else if !tc.shutdown && got != nil {
				t.Fatalf("turn cancellation stopped notification: %v", got)
			}
			once.Do(func() { close(release) })
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("host decision did not complete")
			}
			if !tc.shutdown {
				makeAgent().hostDecision(lifetime, "googl.com")
				if got := calls.Load(); got != 1 {
					t.Fatalf("notification sent %d times; want once", got)
				}
			}
		})
	}
}

func TestSuspiciousNotificationNilLifetimeUsesCaller(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := &config.Config{Tools: config.ToolsConfig{WebFetch: config.WebFetchConfig{Enabled: true, URLPolicy: "denylist"}}}
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	notify := func(ctx context.Context, _ *store.Approval) error { calls.Add(1); cancel(); return ctx.Err() }
	first := &Agent{user: &config.UserConfig{Name: "teo", Role: "child", AgeGroup: "age_5_12"}, cfg: cfg, db: db, onNewSuspiciousApproval: notify}
	first.hostDecision(caller, "googl.com")
	second := &Agent{user: first.user, cfg: cfg, db: db, onNewSuspiciousApproval: func(context.Context, *store.Approval) error { calls.Add(1); return nil }}
	second.hostDecision(context.Background(), "googl.com")
	if calls.Load() != 2 {
		t.Fatalf("canceled legacy caller should allow a later retry; calls=%d", calls.Load())
	}
}
