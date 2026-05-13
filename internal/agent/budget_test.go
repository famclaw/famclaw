package agent

import (
	"testing"

	"github.com/famclaw/famclaw/internal/config"
)

func TestComputeHeadBudget(t *testing.T) {
	tests := []struct {
		name      string
		nCtx      int
		minBudget int
		maxBudget int
	}{
		{
			name:      "typical 32k context",
			nCtx:      32768,
			minBudget: 1024,
			maxBudget: int(0.5 * float64(32768) * 4),
		},
		{
			name:      "small context",
			nCtx:      4096,
			minBudget: 512,
			maxBudget: int(0.5 * float64(4096) * 4),
		},
		{
			name:      "zero context fallback",
			nCtx:      0,
			minBudget: 1,
			maxBudget: -1,
		},
		{
			name:      "tiny context min floor",
			nCtx:      100,
			minBudget: 512,
			maxBudget: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Agent{cfg: &config.Config{LLM: config.LLMConfig{MaxContextTokens: tt.nCtx}}}
			b := computeHeadBudget(a)
			if b < tt.minBudget {
				t.Fatalf("budget %d below min %d", b, tt.minBudget)
			}
			if tt.maxBudget >= 0 && b > tt.maxBudget {
				t.Fatalf("budget %d exceeds max %d", b, tt.maxBudget)
			}
		})
	}
}