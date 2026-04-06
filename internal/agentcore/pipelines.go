package agentcore

import (
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/honeybadger"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// FamilyPipelineDeps holds all dependencies for building a family chat pipeline.
type FamilyPipelineDeps struct {
	Classifier    *classifier.Classifier
	Evaluator     *policy.Evaluator
	DB            *store.DB
	Pool          *mcp.Pool
	ClientFactory func(turn *Turn) *llm.Client
	Temperature   float64
	MaxTokens     int
	ContextWindow int // 0 = use default (4096)
	OnToken       func(string)

	// Security scanning (optional — skipped if nil or unavailable)
	HoneybadgerClient *honeybadger.Client
	SecurityEnabled   bool
	RescanDays        int
	LastScanFunc      func(skillName string) (interface{ Before(interface{}) bool }, bool)
}

// FamilyPipeline assembles the full family chat pipeline:
// classify → policy_input → compress → [security_scan] → llm_call → [tool_loop] → policy_output
func FamilyPipeline(deps FamilyPipelineDeps) Pipeline {
	stages := Pipeline{
		NewStageClassify(deps.Classifier),
		NewStagePolicyInput(deps.Evaluator, deps.DB),
	}

	// Context compression — before LLM call to fit within window
	if deps.ContextWindow > 0 {
		stages = stages.Append(NewStageCompress(deps.ContextWindow))
	}

	// Security scan — before tool execution
	if deps.SecurityEnabled && deps.HoneybadgerClient != nil {
		stages = stages.Append(NewStageSecurityScan(SecurityScanDeps{
			Client:  deps.HoneybadgerClient,
			Enabled: true,
			RescanDays: deps.RescanDays,
		}))
	}

	// LLM call
	stages = stages.Append(NewStageLLMCall(LLMCallDeps{
		ClientFactory: deps.ClientFactory,
		Temperature:   deps.Temperature,
		MaxTokens:     deps.MaxTokens,
		OnToken:       deps.OnToken,
	}))

	// Tool loop only if MCP pool is available
	if deps.Pool != nil {
		stages = stages.Append(NewStageToolLoop(ToolLoopDeps{
			Pool:          deps.Pool,
			ClientFactory: deps.ClientFactory,
			Temperature:   deps.Temperature,
			MaxTokens:     deps.MaxTokens,
		}))
	}

	// Output policy — age-aware filtering (replaces old hardcoded StageOutputFilter)
	stages = stages.Append(NewStagePolicyOutput())

	return stages
}
