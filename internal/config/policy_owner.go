package config

import "sync"

// WebFetchPolicyOwner is the single shared in-process lock ("owner", plan
// §B: "one explicitly injected process-lifetime owner per config path")
// that serializes every in-process read of, and read-modify-write of, this
// Config instance's WebFetch host policy — Tools.WebFetch.URLAllowlist and
// Tools.WebFetch.URLDenylist — across EVERY Agent and every admin handler
// that is constructed from the same *Config.
//
// Root cause it replaces (#423, rounds 16/17): Agent carried a per-instance
// VALUE field `urlDenylistMu sync.RWMutex`, so &parentAgent.urlDenylistMu and
// &childAgent.urlDenylistMu were DIFFERENT mutexes and never mutually
// excluded (verified -race dump: admin.HandleDenylistAdd vs
// childAgent.hostDecision on agent.go:1455/:1502). Attaching one owner to
// the config object itself guarantees that every holder of the same *Config
// reaches the same lock, including Agents, the admin tools, and the web
// settings endpoint — no injection point that a construction site can miss.
//
// Lock order (plan §C, scoped to this owner): the owner is acquired with no
// other lock held; the SharedConfigManager file-commit gate (T) may only be
// taken while the owner is held (a single M -> T edge, never T -> M), so no
// lock cycle is possible. hostDecision takes the owner's read lock alone.
//
// The owner is not YAML state: it is a pure in-process synchronization
// primitive, allocated lazily once per Config instance.
type WebFetchPolicyOwner struct {
	mu sync.RWMutex
}

// RLock acquires the shared owner for decision-time snapshot reads.
func (o *WebFetchPolicyOwner) RLock() { o.mu.RLock() }

// RUnlock releases the shared owner after a decision-time snapshot read.
func (o *WebFetchPolicyOwner) RUnlock() { o.mu.RUnlock() }

// Lock acquires the shared owner exclusively for policy read-modify-writes
// (admin denylist/allowlist handlers, settings publish span).
func (o *WebFetchPolicyOwner) Lock() { o.mu.Lock() }

// Unlock releases the shared owner after a policy read-modify-write.
func (o *WebFetchPolicyOwner) Unlock() { o.mu.Unlock() }
