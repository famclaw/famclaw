package agent

import (
	"testing"

	"github.com/famclaw/famclaw/internal/config"
)

// TestComputeHeadBudget verifies the head-budget formula, its absolute
// floor/ceiling, and that the default production context (131072) yields a
// threshold in the realistic-web-fetch range.
func TestComputeHeadBudget(t *testing.T) {
	tests := []struct {
		name      string
		nCtx      int
		wantMin   int
		wantMax   int
		wantExact int // when set (>0), requires an exact match
	}{
		{
			name:      "default production 128k context — spill engages for realistic web fetches",
			nCtx:      131072,
			wantMin:   4096,  // > 4KB — large enough to be a useful preview
			wantMax:   16384, // < 16KB — small enough that a typical web page spills
			wantExact: 8708,
		},
		{
			name:      "small 8k context — floored to min preview",
			nCtx:      8192,
			wantMin:   512,
			wantMax:   512,
			wantExact: 512,
		},
		{
			name:      "large 1M context — ceiling caps preview at 64KB",
			nCtx:      1000000,
			wantMin:   65536,
			wantMax:   65536,
			wantExact: 65536,
		},
		{
			name:      "zero context falls back to 4096",
			nCtx:      0,
			wantMin:   512,
			wantMax:   512,
			wantExact: 512,
		},
		{
			name:      "tiny 100-token context — floored to min preview",
			nCtx:      100,
			wantMin:   512,
			wantMax:   512,
			wantExact: 512,
		},
		{
			name:      "32k context — scales proportionally",
			nCtx:      32768,
			wantMin:   1024,
			wantMax:   4096,
			wantExact: 2024,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Agent{cfg: &config.Config{LLM: config.LLMConfig{MaxContextTokens: tt.nCtx}}}
			b := computeHeadBudget(a)
			if b < tt.wantMin {
				t.Fatalf("budget %d below min %d", b, tt.wantMin)
			}
			if b > tt.wantMax {
				t.Fatalf("budget %d exceeds max %d", b, tt.wantMax)
			}
			if tt.wantExact > 0 && b != tt.wantExact {
				t.Fatalf("budget %d != exact %d", b, tt.wantExact)
			}
		})
	}
}

// TestComputeHeadBudgetNilAgent ensures the function never panics when
// called with a nil Agent or nil config (defensive — happens in some
// init-order paths).
func TestComputeHeadBudgetNilAgent(t *testing.T) {
	b := computeHeadBudget(nil)
	if b < 512 {
		t.Fatalf("nil agent budget %d below floor 512", b)
	}
}
