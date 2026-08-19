package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gatewayServer boots a fake /v1/model/info gateway and returns its base
// URL, a request counter, and a pointer that can flip the gateway between
// healthy and down for failure/recovery scenarios.
func gatewayServer(models map[string]bool, down *atomic.Bool, count *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if down != nil && down.Load() {
			http.Error(w, "gateway down", http.StatusServiceUnavailable)
			return
		}
		body := `{"data": [
		{"model_name": "fast", "litellm_params": {"merge_reasoning_content_in_choices": false}},
		{"model_name": "council", "litellm_params": {"merge_reasoning_content_in_choices": true}},
		{"model_name": "whisper", "litellm_params": {"model": "openai/whisper-1"}}
	]}`
		// Honour the models argument for shape; the default body covers the
		// assertions below (false-flagged, true-flagged, unflagged).
		_ = models
		w.Write([]byte(body))
	}))
}

func TestReasoningCacheMergeSetting(t *testing.T) {
	var counter atomic.Int32
	server := gatewayServer(nil, nil, &counter)
	defer server.Close()

	yes, no := true, false
	rc := NewReasoningCache(ReasoningAutoDetectConfig{
		Enabled:        true,
		GatewayBaseURL: server.URL,
		PerModelOverride: map[string]bool{
			"pinned": false,
		},
	})

	tests := []struct {
		name  string
		model string
		want  *bool // nil = heuristic fallback expected
	}{
		{"gateway says merge", "council", &yes},
		{"gateway says no merge", "fast", &no},
		{"unflagged model → heuristic", "whisper", nil},
		{"unknown model → heuristic", "never-listed", nil},
		{"override wins over gateway", "council-override", &yes},
		{"override pins false", "pinned", &no},
		{"empty model", "", nil},
	}
	// council-override: override map value for a gateway-known model.
	rc.overrides["council-override"] = true

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rc.MergeSetting(context.Background(), tt.model)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("want nil (heuristic), got %v", *got)
				}
			} else if got == nil || *got != *tt.want {
				t.Fatalf("want %v, got %v", *tt.want, got)
			}
		})
	}
}

func TestReasoningCacheLoadOncePerSession(t *testing.T) {
	var counter atomic.Int32
	server := gatewayServer(nil, nil, &counter)
	defer server.Close()

	rc := NewReasoningCache(ReasoningAutoDetectConfig{Enabled: true, GatewayBaseURL: server.URL})
	for i := 0; i < 50; i++ {
		rc.MergeSetting(context.Background(), "council")
	}
	if n := counter.Load(); n != 1 {
		t.Fatalf("gateway requests = %d, want exactly 1 (load-once per session)", n)
	}
}

func TestReasoningCacheFailureInvalidatesAndRetries(t *testing.T) {
	var counter atomic.Int32
	var down atomic.Bool
	server := gatewayServer(nil, &down, &counter)
	defer server.Close()

	rc := NewReasoningCache(ReasoningAutoDetectConfig{Enabled: true, GatewayBaseURL: server.URL})
	rc.retryBackoff = 20 * time.Millisecond
	// Speed up the transport-side retry for this test: the gateway returns
	// 503 immediately, so the single retry is cheap (only the 1s
	// detectRetryDelay between the two attempts is real cost).

	// First attempt: gateway down → heuristic, one request pair (5s timeout
	// not hit because 503 is instant).
	down.Store(true)
	if got := rc.MergeSetting(context.Background(), "council"); got != nil {
		t.Fatalf("want nil while gateway down, got %v", *got)
	}
	firstCount := counter.Load()
	if firstCount < 1 {
		t.Fatalf("expected at least one request, got %d", firstCount)
	}

	// Within backoff: no further requests, still heuristic.
	rc.MergeSetting(context.Background(), "council")
	if counter.Load() != firstCount {
		t.Fatalf("gateway re-queried inside backoff (count %d → %d)", firstCount, counter.Load())
	}

	// Gateway recovers; after the backoff the next request retries and
	// caches the answer.
	down.Store(false)
	time.Sleep(30 * time.Millisecond)
	yes := true
	if got := rc.MergeSetting(context.Background(), "council"); got == nil || *got != yes {
		t.Fatalf("want council=true after recovery, got %v", got)
	}
	if counter.Load() == firstCount {
		t.Fatal("gateway was never re-queried after recovery")
	}
}

func TestReasoningCacheDisabledMakesNoRequests(t *testing.T) {
	var counter atomic.Int32
	server := gatewayServer(nil, nil, &counter)
	defer server.Close()

	rc := NewReasoningCache(ReasoningAutoDetectConfig{GatewayBaseURL: server.URL}) // Enabled=false
	if got := rc.MergeSetting(context.Background(), "council"); got != nil {
		t.Fatalf("disabled cache must fall back to heuristic, got %v", *got)
	}
	if n := counter.Load(); n != 0 {
		t.Fatalf("disabled cache made %d gateway requests", n)
	}
	if _, err := rc.Warm(context.Background()); err != nil {
		t.Fatalf("Warm on disabled cache: %v", err)
	}
}

func TestReasoningCacheNilURLIsInert(t *testing.T) {
	rc := NewReasoningCache(ReasoningAutoDetectConfig{Enabled: true})
	if rc.Enabled() {
		t.Fatal("cache with no gateway URL must be inert")
	}
	if got := rc.MergeSetting(context.Background(), "council"); got != nil {
		t.Fatalf("inert cache must return nil, got %v", *got)
	}
}

func TestReasoningCacheNilReceiver(t *testing.T) {
	var rc *ReasoningCache
	if got := rc.MergeSetting(context.Background(), "council"); got != nil {
		t.Fatalf("nil cache must return nil, got %v", *got)
	}
	if rc.Enabled() {
		t.Fatal("nil cache is not enabled")
	}
}

func TestReasoningCacheWarm(t *testing.T) {
	var counter atomic.Int32
	server := gatewayServer(nil, nil, &counter)
	defer server.Close()

	rc := NewReasoningCache(ReasoningAutoDetectConfig{Enabled: true, GatewayBaseURL: server.URL})
	n, err := rc.Warm(context.Background())
	if err != nil {
		t.Fatalf("Warm: %v", err)
	}
	// The fake gateway lists three models.
	if n != 3 {
		t.Fatalf("Warm discovered %d models, want 3", n)
	}
	// Subsequent lookups must not re-query.
	before := counter.Load()
	rc.MergeSetting(context.Background(), "council")
	if counter.Load() != before {
		t.Fatal("lookup after successful Warm re-queried the gateway")
	}
}

func TestReasoningCacheThreadSafe(t *testing.T) {
	var counter atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		// A little latency so concurrent first-lookups race on the
		// cache lock and the fetch path.
		time.Sleep(2 * time.Millisecond)
		w.Write([]byte(`{"data":[{"model_name":"council","litellm_params":{"merge_reasoning_content_in_choices":true}}]}`))
	}))
	defer server.Close()

	rc := NewReasoningCache(ReasoningAutoDetectConfig{Enabled: true, GatewayBaseURL: server.URL})
	rc.retryBackoff = time.Hour // failures must not cascade into retries here

	const workers = 32
	const callsPerWorker = 50
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < callsPerWorker; j++ {
				_ = rc.MergeSetting(context.Background(), "council")
			}
		}()
	}
	wg.Wait()

	// Every successful-or-failing path must have been served without
	// panic (race detector covers the lock); the counter just documents
	// that lookups happened.
	if counter.Load() == 0 {
		t.Fatal("no gateway requests were made")
	}
}

// TestReasoningCacheWaitersNotBlocked verifies that while one request is
// fetching detection data, all other lookups return nil immediately instead
// of queueing behind the (up to ~12s) HTTP call. The test is deterministic:
// it waits until the initiating fetch is actually in-flight (the gateway
// signals its first request) before timing the waiters, so exactly one
// caller is the blocked initiator and the rest hit the `detecting` branch.
func TestReasoningCacheWaitersNotBlocked(t *testing.T) {
	var counter atomic.Int32
	gated := make(chan struct{})
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if counter.Add(1) == 1 {
			started <- struct{}{} // detection fetch is now in-flight
		}
		// Hold the gateway request until the test releases it.
		<-gated
		w.Write([]byte(`{"data":[{"model_name":"council","litellm_params":{"merge_reasoning_content_in_choices":true}}]}`))
	}))
	defer server.Close()

	rc := NewReasoningCache(ReasoningAutoDetectConfig{Enabled: true, GatewayBaseURL: server.URL})

	// Initiating call: blocked in the HTTP fetch.
	done := make(chan *bool, 1)
	go func() {
		done <- rc.MergeSetting(context.Background(), "council")
	}()

	// Wait until the initiator's fetch is confirmed in-flight, so the
	// waiters below all see detecting=true (no one races to be initiator).
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("initiator's detection fetch never started")
	}

	// Waiters: must NOT block on the in-flight fetch.
	const waiters = 20
	start := time.Now()
	for i := 0; i < waiters; i++ {
		if got := rc.MergeSetting(context.Background(), "council"); got != nil {
			t.Fatalf("waiter %d: want nil while detection is in flight, got %v", i, *got)
		}
	}
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("waiters took %v; they must return nil immediately, not queue behind the fetch", elapsed)
	}

	// Release the fetch; the initiator must then see the authoritative flag.
	close(gated)
	select {
	case got := <-done:
		if got == nil || !*got {
			t.Fatalf("initiator: want council=true after fetch completes, got %v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("initiator did not return after the fetch was released")
	}
}

// TestReasoningCacheCallerCanceledDoesNotPrimeBackoff verifies that a
// canceled caller context (chat turn aborted mid-detection) is not treated
// as a gateway failure: no backoff is primed, so the next chat turn
// re-attempts detection immediately instead of sitting on the heuristic
// for the backoff window.
func TestReasoningCacheCallerCanceledDoesNotPrimeBackoff(t *testing.T) {
	var counter atomic.Int32
	server := gatewayServer(nil, nil, &counter)
	defer server.Close()

	rc := NewReasoningCache(ReasoningAutoDetectConfig{Enabled: true, GatewayBaseURL: server.URL})
	rc.retryBackoff = time.Hour // if it were primed, the retry below would be skipped

	// Simulate a canceled chat turn while detection is in flight.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := rc.MergeSetting(canceled, "council"); got != nil {
		t.Fatalf("canceled caller: want nil (heuristic), got %v", *got)
	}
	if !rc.nextAttempt.IsZero() {
		t.Fatalf("canceled caller primed backoff until %v; next chat must re-attempt immediately", rc.nextAttempt)
	}

	// The gateway is healthy: the very next lookup must reach it and get
	// the authoritative answer — no backoff sleep in between.
	yes := true
	if got := rc.MergeSetting(context.Background(), "council"); got == nil || *got != yes {
		t.Fatalf("next turn: want council=true via fresh detection, got %v", got)
	}
	if counter.Load() == 0 {
		t.Fatal("next turn did not re-query the gateway; backoff is still in effect")
	}
}
