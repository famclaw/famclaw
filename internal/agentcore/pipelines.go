package agentcore

import (
	"github.com/famclaw/famclaw/internal/classifier"
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
	OnToken       func(string)
}

// FamilyPipeline assembles the full family chat pipeline:
// classify → policy_input → llm_call → tool_loop → output_filter
func FamilyPipeline(deps FamilyPipelineDeps) Pipeline {
	stages := Pipeline{
		NewStageClassify(deps.Classifier),
		NewStagePolicyInput(deps.Evaluator, deps.DB),
		NewStageLLMCall(LLMCallDeps{
			ClientFactory: deps.ClientFactory,
			Temperature:   deps.Temperature,
			MaxTokens:     deps.MaxTokens,
			OnToken:       deps.OnToken,
		}),
	}

	// Tool loop only if MCP pool is available
	if deps.Pool != nil {
		stages = stages.Append(NewStageToolLoop(ToolLoopDeps{
			Pool:          deps.Pool,
			ClientFactory: deps.ClientFactory,
			Temperature:   deps.Temperature,
			MaxTokens:     deps.MaxTokens,
		}))
	}

	stages = stages.Append(NewStageOutputFilter())

	return stages
}
