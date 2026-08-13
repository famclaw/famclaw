// Package reload provides a config hot-reload contract for FamClaw.
//
// Components that can be safely rebuilt at runtime implement the Reloader
// interface. A Registry collects them at construction time; when the config
// file changes, the registry's Reload method walks every component and
// reports the outcome of each — so the log never claims success for work
// that was not actually done. Components that cannot be hot-reloaded are
// declared via RequireRestart and reported as "requires_restart" instead.
package reload

import (
	"log"
	"sync"

	"github.com/famclaw/famclaw/internal/config"
)

// Reloader is implemented by components that can have their configuration
// refreshed at runtime without a full process restart.
type Reloader interface {
	// ReloadConfig applies the new configuration. Implementations must be
	// safe for concurrent use — the running server may be handling messages
	// while reload happens. Returning an error means the component could
	// not be rebuilt; the caller should report it as failed.
	ReloadConfig(*config.Config) error
}

// Outcome describes the result of reloading a single component.
type Outcome string

const (
	// OutcomeReloaded means the component was successfully rebuilt.
	OutcomeReloaded Outcome = "reloaded"
	// OutcomeFailed means ReloadConfig returned an error.
	OutcomeFailed Outcome = "failed"
	// OutcomeRequiresRestart means the component cannot be hot-reloaded;
	// a full process restart is needed for its config changes to take effect.
	OutcomeRequiresRestart Outcome = "requires_restart"
)

// Status reports the outcome of reloading a single component.
type Status struct {
	Name    string  `json:"name"`
	Outcome Outcome `json:"outcome"`
	Detail  string  `json:"detail,omitempty"`
}

type namedReloader struct {
	name     string
	reloader Reloader
}

// RestartRequirement declares a component that cannot be hot-reloaded and
// therefore requires a full process restart for config changes to take effect.
type RestartRequirement struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Registry collects hot-reloadable components and restart requirements.
// Components join at construction time. The reload loop iterates the registry
// and reports per-component success/failure.
type Registry struct {
	mu           sync.Mutex
	reloaders    []namedReloader
	requirements []RestartRequirement
}

// NewRegistry creates an empty reload registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds a hot-reloadable component. Safe to call from multiple
// goroutines but is typically called once during construction.
func (r *Registry) Register(name string, reloader Reloader) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloaders = append(r.reloaders, namedReloader{name: name, reloader: reloader})
}

// RequireRestart declares that a component cannot be hot-reloaded and
// therefore requires a full process restart for config changes to take effect.
func (r *Registry) RequireRestart(name, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requirements = append(r.requirements, RestartRequirement{Name: name, Reason: reason})
}

// Count returns the total number of registered components (reloaders + restart requirements).
func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reloaders) + len(r.requirements)
}

// Reload runs ReloadConfig on every registered component and returns a
// Status for each. Reloaders that succeed are reported as "reloaded";
// reloaders that error are "failed"; declared restart requirements are
// "requires_restart".
func (r *Registry) Reload(cfg *config.Config) []Status {
	r.mu.Lock()
	reloaders := make([]namedReloader, len(r.reloaders))
	copy(reloaders, r.reloaders)
	reqs := make([]RestartRequirement, len(r.requirements))
	copy(reqs, r.requirements)
	r.mu.Unlock()

	statuses := make([]Status, 0, len(reloaders)+len(reqs))
	for _, nr := range reloaders {
		err := nr.reloader.ReloadConfig(cfg)
		if err != nil {
			log.Printf("[reload] %s: FAILED — %v", nr.name, err)
			statuses = append(statuses, Status{
				Name:    nr.name,
				Outcome: OutcomeFailed,
				Detail:  err.Error(),
			})
			continue
		}
		log.Printf("[reload] %s: reloaded ✅", nr.name)
		statuses = append(statuses, Status{
			Name:    nr.name,
			Outcome: OutcomeReloaded,
		})
	}
	for _, req := range reqs {
		log.Printf("[reload] %s: requires restart — %s", req.Name, req.Reason)
		statuses = append(statuses, Status{
			Name:    req.Name,
			Outcome: OutcomeRequiresRestart,
			Detail:  req.Reason,
		})
	}
	return statuses
}
