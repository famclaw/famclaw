package agent

import (
	"testing"

	"github.com/famclaw/famclaw/internal/config"
)

func TestComputeHeadBudgetTypicalConfig(t *testing.T) {
	a := &Agent{cfg: &config.Config{LLM: config.LLMConfig{MaxContextTokens: 32768}}}
	b := computeHeadBudget(a)
	if b < 1024 {
		t.Errorf("budget too small for 32k ctx: %d", b)
	}
	// Safety floor: never exceed 50% of n_ctx in bytes
	floor := int(0.5 * float64(32768) * 4)
	if b > floor {
		t.Errorf("budget %d exceeds 50%% floor %d", b, floor)
	}
}

func TestComputeHeadBudgetSmallCtx(t *testing.T) {
	a := &Agent{cfg: &config.Config{LLM: config.LLMConfig{MaxContextTokens: 4096}}}
	b := computeHeadBudget(a)
	if b < 512 {
		t.Errorf("budget under 512 floor: %d", b)
	}
}

func TestComputeHeadBudgetZeroCtxFallsBack(t *testing.T) {
	a := &Agent{cfg: &config.Config{LLM: config.LLMConfig{MaxContextTokens: 0}}}
	b := computeHeadBudget(a)
	if b <= 0 {
		t.Errorf("budget should be positive even with zero ctx: %d", b)
	}
}

func TestComputeHeadBudgetMinFloor(t *testing.T) {
	a := &Agent{cfg: &config.Config{LLM: config.LLMConfig{MaxContextTokens: 100}}}
	b := computeHeadBudget(a)
	if b < 512 {
		t.Errorf("budget %d below 512 floor for tiny ctx", b)
	}
}
