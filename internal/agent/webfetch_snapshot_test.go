package agent

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/webfetch"
)

// A settings change during a fetch must not change its fallback decision.
func TestWebFetchFallbackUsesConfigSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		initial, updated       bool
		initialMin, updatedMin int
		want                   int
	}{
		{"enabled then disabled", true, false, 10, 100, 1},
		{"disabled then enabled", false, true, 10, 100, 0},
		{"threshold changes only", true, true, 1, 100, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeBrowserPool{text: "fake rendered page", bytes: 10}
			a := newBotBlockAgent(0, tc.initial, fake)
			a.cfg.Tools.WebFetch.FallbackMinTextLength = tc.initialMin
			started := make(chan struct{})
			release := make(chan struct{})
			done := make(chan error, 1)
			var once sync.Once
			a.webFetcher = func(context.Context, string, webfetch.Options) (*webfetch.Result, error) {
				close(started)
				<-release
				return &webfetch.Result{URL: "https://example.com/", StatusCode: 200, Text: "ok"}, nil
			}
			go func() {
				_, err := a.handleWebFetch(context.Background(), map[string]any{"url": "https://example.com/"})
				done <- err
			}()
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("fake fetch did not start")
			}
			owner := a.cfg.WebFetchPolicyOwner()
			owner.Lock()
			updated := a.cfg.Tools.WebFetch
			updated.FallbackToBrowser = tc.updated
			updated.FallbackMinTextLength = tc.updatedMin
			a.cfg.Tools.WebFetch = updated
			owner.Unlock()
			once.Do(func() { close(release) })
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("fetch did not finish")
			}
			if got := len(fake.calls); got != tc.want {
				t.Fatalf("fallback calls=%d want=%d based on initial config", got, tc.want)
			}
		})
	}
}

// The settings writer replaces the same WebFetch struct under the policy lock.
func TestWebFetchConcurrentSettingsSnapshot(t *testing.T) {
	if os.Getenv("FAMCLAW_TEST_FETCH_SNAPSHOT") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestWebFetchConcurrentSettingsSnapshot$")
		cmd.Env = append(os.Environ(), "FAMCLAW_TEST_FETCH_SNAPSHOT=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child failed: %v\n%s", err, out)
		}
		return
	}
	log.SetOutput(io.Discard)
	a := newBotBlockAgent(0, true, nil)
	a.webFetcher = func(context.Context, string, webfetch.Options) (*webfetch.Result, error) {
		return &webfetch.Result{URL: "https://example.com/", StatusCode: 200, Text: "ok"}, nil
	}
	owner := a.cfg.WebFetchPolicyOwner()
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 3000; i++ {
			owner.Lock()
			updated := a.cfg.Tools.WebFetch
			updated.FallbackToBrowser = i%2 == 0
			updated.FallbackMinTextLength = 10 + i%2
			a.cfg.Tools.WebFetch = updated
			owner.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 3000; i++ {
			if _, err := a.handleWebFetch(context.Background(), map[string]any{"url": "https://example.com/"}); err != nil {
				t.Error(fmt.Errorf("fetch: %w", err))
				return
			}
		}
	}()
	close(start)
	wg.Wait()
}
