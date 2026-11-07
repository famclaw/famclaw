package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/agent"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/gorilla/websocket"
)

// A child closing chat must not cancel delivery of the parent's approval links.
func TestApprovalSurvivesChatDisconnect(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessions := store.NewSessionStore(db.SQL())
	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Server: config.ServerConfig{Secret: "test-secret", MDNSName: "famclaw"}, Users: []config.UserConfig{{Name: "parent", Role: "parent"}, {Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"}}}
	cfg.Tools.SandboxRoot = t.TempDir()
	ids := identity.NewStore(db)
	if err := ids.LinkAccount("parent", "telegram", "parent-id"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	result := make(chan error, 1)
	messages := make(chan string, 1)
	notifier := notify.NewMultiNotifier(cfg, ids, func(ctx context.Context, _, _, text string) error {
		close(entered)
		<-release
		messages <- text
		result <- ctx.Err()
		return ctx.Err()
	})
	lifetime, cancel := context.WithCancel(context.Background())
	s := NewServer(lifetime, cfg, "", db, sessions, nil, ids, ev, classifier.New(), notifier, nil, nil, nil)
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		cancel()
		waitApprovalCleanup(t, s)
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	session, err := sessions.Create(context.Background(), "emma", "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(t, ts, "/api/chat", ""), http.Header{"Cookie": []string{"famclaw_session=" + session}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.WriteJSON(WsMessage{Type: "chat", Payload: json.RawMessage(`{"text":"can I use instagram and tiktok"}`)}); err != nil {
		t.Fatal(err)
	}
	readWSMessage(t, conn, "message", 5*time.Second)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("parent notification not started")
	}
	_ = conn.Close()
	// The handler removes the socket only after its request context is cancelled.
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.clientsMu.RLock()
		n := len(s.clients)
		s.clientsMu.RUnlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("chat handler did not exit")
		}
		time.Sleep(time.Millisecond)
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-result; err != nil {
		t.Fatalf("parent notification canceled by child disconnect: %v", err)
	}
	text := <-messages
	if !strings.Contains(text, "action=approve") || !strings.Contains(text, "action=deny") {
		t.Fatalf("missing approval links: %q", text)
	}
	a, err := db.GetApproval(context.Background(), agent.ApprovalID("emma", "social_media"))
	if err != nil {
		t.Fatal(err)
	}
	if a == nil {
		t.Fatal("approval row missing")
	}
}

func TestApprovalServerShutdown(t *testing.T) {
	t.Run("pending database operation", func(t *testing.T) {
		db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		db.SQL().SetMaxOpenConns(1)
		held, err := db.SQL().Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = held.Close() })
		lifetime, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		cfg := &config.Config{Users: []config.UserConfig{{Name: "emma", Role: "child"}}}
		s := NewServer(lifetime, cfg, "", db, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		t.Cleanup(func() { cancel(); _ = held.Close(); waitApprovalCleanup(t, s) })
		s.enqueueApproval(&cfg.Users[0], "social_media", "question")
		deadline := time.Now().Add(3 * time.Second)
		for db.SQL().Stats().WaitCount == 0 {
			if time.Now().After(deadline) {
				t.Fatal("approval did not wait for the database")
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
		ctx, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		if err := s.WaitForBackground(ctx); err != nil {
			t.Fatal(err)
		}
		if err := held.Close(); err != nil {
			t.Fatal(err)
		}
		a, err := db.GetApproval(ctx, agent.ApprovalID("emma", "social_media"))
		if err != nil {
			t.Fatal(err)
		}
		if a != nil {
			t.Fatal("approval persisted after server cancellation")
		}
	})
	t.Run("notification in flight", func(t *testing.T) {
		db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		cfg := &config.Config{Users: []config.UserConfig{{Name: "parent", Role: "parent"}, {Name: "emma", Role: "child"}}}
		ids := identity.NewStore(db)
		if err := ids.LinkAccount("parent", "telegram", "parent-id"); err != nil {
			t.Fatal(err)
		}
		entered := make(chan struct{})
		stopped := make(chan error, 1)
		deadlineObserved := make(chan time.Duration, 1)
		notifier := notify.NewMultiNotifier(cfg, ids, func(ctx context.Context, _, _, _ string) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				deadlineObserved <- 0
			} else {
				deadlineObserved <- time.Until(deadline)
			}
			close(entered)
			<-ctx.Done()
			stopped <- ctx.Err()
			return ctx.Err()
		})
		lifetime, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		s := NewServer(lifetime, cfg, "", db, nil, nil, ids, nil, nil, notifier, nil, nil, nil)
		t.Cleanup(func() { cancel(); waitApprovalCleanup(t, s) })
		s.enqueueApproval(&cfg.Users[1], "social_media", "question")
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("notification did not start")
		}
		if remaining := <-deadlineObserved; remaining <= 0 || remaining > 30*time.Second {
			t.Fatalf("notification deadline remaining: %v", remaining)
		}
		cancel()
		select {
		case err := <-stopped:
			if err != context.Canceled {
				t.Fatalf("notification context error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("notification did not stop")
		}
		ctx, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		if err := s.WaitForBackground(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

func waitApprovalCleanup(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.WaitForBackground(ctx); err != nil {
		t.Errorf("background cleanup: %v", err)
	}
}

func TestBackgroundSpawnAfterShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Server{lifetimeCtx: ctx}
	called := make(chan struct{}, 1)
	s.spawnBackground(func() { called <- struct{}{} })
	wait, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	if err := s.WaitForBackground(wait); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
		t.Fatal("background work started after shutdown")
	default:
	}
}
