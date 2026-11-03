package agent

// Review artifact (untracked) — exact-head review t_4c0383fc, candidate 9347a6a.
//
// TestReviewTwoAgentSharedConfigRace reconstructs the RED evidence preserved in
// the builder worktree (diagnostic: shared_cfg_race_test.go:20-43, one config
// shared by two literal Agents, parent's lock passed to admin, child decision
// run concurrently). It is the race #423 must eliminate; the candidate's
// committed TestSharedConfigTwoAgentDenylistRace does NOT exercise this path.
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

	deps := admin.Deps{
		Cfg:           cfg,
		Actor:         "parent",
		Gateway:       "test",
		URLDenylistMu: &parentAgent.urlDenylistMu,
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