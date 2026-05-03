package agentcore

import (
	"context"
	"time"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/honeybadger"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/skillbridge"
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
	ContextWindow int
	OnToken       func(string)

	// Builtin tool handler (spawn_agent, etc.)
	BuiltinHandler func(ctx context.Context, name string, args map[string]any) (string, error)

	// Security scanning (async quarantine pattern)
	Quarantine     *skillbridge.Quarantine
	Scanner        skillbridge.Scanner
	HBClient       *honeybadger.Client
	RuntimeScan    bool
	RescanInterval time.Duration
	ScanTimeout    time.Duration
	Paranoia       string
	BlockOnFail    bool
	NotifyOnBlock  bool
	NotifyFunc     func(title, body string)
	LastScanFunc   func(scanTarget string) (time.Time, bool)
	SaveScanFunc   func(scanTarget string, result *honeybadger.ScanResult)
}

// FamilyPipeline assembles the full family chat pipeline:
// classify → policy_input → compress → quarantine_filter → llm_call → [tool_loop] → policy_output → async_scan
func FamilyPipeline(deps FamilyPipelineDeps) Pipeline {
	stages := Pipeline{
		NewStageClassify(deps.Classifier),
		NewStagePolicyInput(deps.Evaluator, deps.DB),
	}

	// Context compression before LLM call
	if deps.ContextWindow > 0 {
		stages = stages.Append(NewStageCompress(deps.ContextWindow))
	}

	// Quarantine filter — remove blocked tools before LLM sees them (microsecond map lookup)
	if deps.Quarantine != nil {
		stages = stages.Append(NewStageQuarantineFilter(deps.Quarantine))
	}

	// LLM call
	stages = stages.Append(NewStageLLMCall(LLMCallDeps{
		ClientFactory: deps.ClientFactory,
		Temperature:   deps.Temperature,
		MaxTokens:     deps.MaxTokens,
		OnToken:       deps.OnToken,
	}))

	// Tool loop
	if deps.Pool != nil || deps.BuiltinHandler != nil {
		stages = stages.Append(NewStageToolLoop(ToolLoopDeps{
			Pool:            deps.Pool,
			ClientFactory:   deps.ClientFactory,
			Temperature:     deps.Temperature,
			MaxTokens:       deps.MaxTokens,
			BuiltinHandler:  deps.BuiltinHandler,
			PolicyEvaluator: deps.Evaluator,
		}))
	}

	// Output policy
	stages = stages.Append(NewStagePolicyOutput())

	// Async security scan — fires goroutines, returns immediately, never blocks the turn
	if deps.RuntimeScan && deps.Scanner != nil {
		stages = stages.Append(NewStageAsyncScan(AsyncScanDeps{
			Scanner:        deps.Scanner,
			Quarantine:     deps.Quarantine,
			LastScanFunc:   deps.LastScanFunc,
			SaveScanFunc:   deps.SaveScanFunc,
			RescanInterval: deps.RescanInterval,
			ScanTimeout:    deps.ScanTimeout,
			Paranoia:       deps.Paranoia,
			Enabled:        true,
			BlockOnFail:    deps.BlockOnFail,
			NotifyOnBlock:  deps.NotifyOnBlock,
			NotifyFunc:     deps.NotifyFunc,
		}))
	}

	return stages
}
