package llm

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// ReasoningAutoDetectConfig configures gateway auto-detection of each
// model's merge_reasoning_content_in_choices setting.
type ReasoningAutoDetectConfig struct {
	// Enabled turns auto-detection on. When false (or when the cache is
	// not attached to a client) the built-in heuristic decides every merge.
	Enabled bool

	// GatewayBaseURL is the LLM endpoint to query for model metadata.
	// By default it is the configured LLM endpoint base URL (it IS the
	// gateway); an explicit litellm_url in config wins.
	GatewayBaseURL string

	// GatewayAPIKey is the bearer key for the detection endpoint.
	// Defaults to the LLM endpoint's API key.
	GatewayAPIKey string

	// PerModelOverride holds explicit per-model
	// merge_reasoning_content_in_choices settings. They take precedence
	// over whatever the gateway reports.
	PerModelOverride map[string]bool
}

// defaultRetryBackoff is how long to wait after a failed detection before
// the next request re-tries the gateway. A failed attempt costs up to
// ~11s (5s timeout + 1s + 5s), so re-trying on every message would stall
// the family's chat while the gateway is down.
const defaultRetryBackoff = 30 * time.Second

// ReasoningCache answers "does this model's reasoning field carry the final
// answer?" with a gateway-authoritative setting when available, and nil
// (fall back to the heuristic) when it does not.
//
// The cache is process-wide and shared by every LLM client: it loads the
// gateway's model settings once per session and serves subsequent lookups
// from memory. On detection failure it invalidates itself so a later
// request (after the backoff elapses) re-tries — a gateway that comes back
// online is picked up without a famclaw restart.
//
// A detection in flight never blocks other lookups: concurrent
// MergeSetting calls see the `detecting` state and return nil immediately
// instead of piling up behind the (up to ~12s) HTTP fetch. Only the
// initiating request pays the fetch cost.
type ReasoningCache struct {
	mu          sync.Mutex
	enabled     bool
	client      *LiteLLMClient
	gatewayBase string
	overrides   map[string]bool

	// settings holds model name → *bool (nil = gateway reported no flag)
	// once a fetch has succeeded. loaded=true means the gateway answered.
	settings map[string]*bool
	loaded   bool

	// detecting guards the in-flight fetch so concurrent callers return
	// nil instead of waiting on the network.
	detecting bool

	// nextAttempt gates re-tries after a failed fetch.
	nextAttempt time.Time

	retryBackoff time.Duration
}

// NewReasoningCache builds a cache from cfg. A nil or disabled cache is
// inert: every MergeSetting call returns nil and no HTTP happens.
func NewReasoningCache(cfg ReasoningAutoDetectConfig) *ReasoningCache {
	rc := &ReasoningCache{
		enabled:      cfg.Enabled,
		overrides:    cfg.PerModelOverride,
		retryBackoff: defaultRetryBackoff,
	}
	if cfg.Enabled && strings.TrimSpace(cfg.GatewayBaseURL) != "" {
		rc.client = NewLiteLLMClient(cfg.GatewayBaseURL, cfg.GatewayAPIKey)
		rc.gatewayBase = strings.TrimRight(strings.TrimSpace(cfg.GatewayBaseURL), "/")
	}
	return rc
}

// Enabled reports whether auto-detection is active.
func (rc *ReasoningCache) Enabled() bool {
	if rc == nil {
		return false
	}
	return rc.enabled && rc.client != nil
}

// Warm runs a detection fetch now (ignoring the backoff) so startup can log
// the outcome before the first message arrives. It returns the number of
// models the gateway reported; an error means the gateway was unreachable
// or its response was unusable and the heuristic is in force until a retry
// succeeds.
func (rc *ReasoningCache) Warm(ctx context.Context) (int, error) {
	if rc == nil || !rc.Enabled() {
		return 0, nil
	}
	if err := rc.detect(ctx); err != nil {
		return 0, err
	}
	rc.mu.Lock()
	n := len(rc.settings)
	rc.mu.Unlock()
	return n, nil
}

// MergeSetting returns the authoritative merge_reasoning_content_in_choices
// setting for model, or nil when no gateway answer is known (detection
// disabled, model not on the gateway, or the gateway unreachable) — in
// which case the caller applies the built-in heuristic.
//
// An explicit PerModelOverride always wins and never touches the network.
//
// MergeSetting never blocks behind an in-flight detection: when another
// request is fetching, it returns nil so the caller can proceed with the
// heuristic instead of stalling on the gateway.
func (rc *ReasoningCache) MergeSetting(ctx context.Context, model string) *bool {
	if model == "" {
		return nil
	}
	if rc == nil {
		return nil
	}
	if v, ok := rc.overrides[model]; ok {
		return &v
	}

	rc.mu.Lock()
	if !rc.Enabled() {
		rc.mu.Unlock()
		return nil
	}
	if rc.loaded {
		// The gateway answered this session: trust it (or the heuristic,
		// when it knew no setting for this model).
		v := rc.settings[model]
		rc.mu.Unlock()
		return v
	}
	if time.Now().Before(rc.nextAttempt) {
		// Still in backoff after a failed attempt — heuristic for now.
		rc.mu.Unlock()
		return nil
	}
	if rc.detecting {
		// A fetch is in flight; don't queue behind its network time.
		rc.mu.Unlock()
		return nil
	}
	rc.detecting = true
	rc.mu.Unlock()

	// Run the fetch WITHOUT holding the lock so concurrent lookups keep
	// flowing (they hit the `detecting` branch above and return nil).
	if err := rc.detect(ctx); err != nil {
		rc.mu.Lock()
		rc.loaded = false
		rc.detecting = false
		if ctx.Err() == nil {
			// A genuine gateway failure — back off so the next few
			// lookups use the heuristic instead of re-paying the fetch
			// cost while the gateway is down.
			rc.nextAttempt = time.Now().Add(rc.retryBackoff)
			rc.mu.Unlock()
			log.Printf("[llm] auto-detect: gateway %s unreachable (%v) — using heuristic reasoning merge", rc.gatewayBase, err)
		} else {
			// The caller's context was canceled (chat turn aborted,
			// shutdown): the gateway may be perfectly healthy. Leave
			// nextAttempt unset so the next chat turn re-attempts
			// detection immediately.
			rc.mu.Unlock()
		}
		return nil
	}
	rc.mu.Lock()
	v := rc.settings[model]
	rc.detecting = false
	rc.mu.Unlock()
	return v
}

// detect runs the fetch and publishes the outcome under the lock. It is
// safe to call concurrently; publication is idempotent.
func (rc *ReasoningCache) detect(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Bound the detection attempt itself so a pathological gateway can
	// never pin a chat turn past the request timeout.
	detectCtx, cancel := context.WithTimeout(ctx, 2*detectRequestTimeout+detectRetryDelay+time.Second)
	defer cancel()

	m, err := rc.client.MergeSettings(detectCtx)
	if err != nil {
		return err
	}
	rc.mu.Lock()
	rc.settings = m
	rc.loaded = true
	rc.nextAttempt = time.Time{}
	rc.mu.Unlock()
	return nil
}
