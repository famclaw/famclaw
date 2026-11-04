package agent

// Review artifact (untracked) — exact-head review t_4c0383fc, candidate 9347a6a.
//
// TestReviewTwoAgentSharedConfigRace reconstructs the RED evidence preserved in
// the builder worktree (diagnostic: shared_cfg_race_test.go:20-43, one config
// shared by two literal Agents, parent's lock passed to admin, child decision
// run concurrently). It is the race #423 must eliminate; the candidate's
// committed TestSharedConfigTwoAgentDenylistRace does NOT exercise this path.
//
// #423 round 18: admin.Deps now carries the config's ONE shared
// WebFetchPolicyOwner (cfg.WebFetchPolicyOwner()) instead of a per-Agent
// value mutex, so both Agents' hostDecision paths and this admin writer
// lock the SAME owner.
import (
	"context"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/agent/tools/admin"
	"github.com/famclaw/famclaw/internal/config"
)

func TestReviewTwoAgentSharedConfigRace(t *testing.T) {
	parentUser := &config.UserConfig{Name: "parent", Role: "parent"}
	childUser := &config.UserConfig{Name: "child", Role: "child"}
	cfg := &config.Config{}

	parentAgent, err := NewAgent(parentUser, cfg, nil, nil, nil, nil, AgentDeps{})
	if err != nil {
		t.Fatal(err)
	}
	childAgent, err := NewAgent(childUser, cfg, nil, nil, nil, nil, AgentDeps{})
	if err != nil {
		t.Fatal(err)
	}

	// The #423 round 18 invariant this test reconstructs: two Agents built
	// from the SAME *config.Config must reach the SAME shared policy owner
	// (the rounds 16/17 failure was a per-Agent value mutex — two different
	// locks, so the admin writer and the child's decision never excluded).
	if parentAgent.sharedPolicyOwner() != childAgent.sharedPolicyOwner() {
		t.Fatalf("two Agents over one config do not share a policy owner: parent=%p child=%p",
			parentAgent.sharedPolicyOwner(), childAgent.sharedPolicyOwner())
	}

	deps := admin.Deps{
		Cfg:     cfg,
		Actor:   "parent",
		Gateway: "test",
		// #423 round 18: the shared owner now lives on the config — the
		// same instance both Agents' hostDecision paths lock (parent
		// writer vs child decision must mutually exclude on ONE owner).
		SharedPolicyOwner: cfg.WebFetchPolicyOwner(),
	}

	var wg sync.WaitGroup
	const n = 50
	for i := 0; i < n; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			_, _ = admin.HandleDenylistAdd(context.Background(), deps, map[string]any{"host": "review-race.example"})
		}()
		go func() {
			defer wg.Done()
			_, _ = admin.HandleDenylistRemove(context.Background(), deps, map[string]any{"host": "review-race.example"})
		}()
		go func() {
			defer wg.Done()
			_, _ = childAgent.hostDecision(context.Background(), "review-race.example")
		}()
	}
	wg.Wait()
}
