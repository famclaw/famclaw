# Agent Core Rewrite — Design Spec

**Date:** 2026-04-05
**Status:** Draft
**Scope:** Pipeline architecture, tool calling, skills/adapters, subagents, context compression, honeybadger integration

## Goals

1. Replace the monolithic agent loop with a composable pipeline of stages
2. Fix tool calling — inject schemas into LLM requests so small models can actually call tools
3. Smart tool selection to stay within context budgets (no 30-60K token tool dumps)
4. Support plugins from FamClaw, OpenClaw, and Claude Code ecosystems via adapters
5. Subagent dispatching with explicit LLM profile control and parent approval
6. Tiered context compression that auto-adapts to detected context window size
7. Honeybadger security gate for skill/plugin installation and runtime
8. Family chat first, but agent loop reusable for CLI/power-user modes

## Non-Goals

- Auto-routing subagents to LLM tiers (parent decides explicitly)
- Full Claude Code feature parity (no git worktrees, no IDE integration)
- Custom pipeline DSL or YAML-defined pipelines (pipelines assembled in Go code)

---

## 1. Pipeline Architecture

### 1.1 Core Types

```go
// Turn holds all state for one user message through the pipeline.
type Turn struct {
    User        *config.UserConfig
    Input       string
    Messages    []llm.Message       // conversation history being built
    Category    classifier.Category
    Policy      policy.Decision
    Tools       []Tool              // available tools (after filtering)
    ToolCalls   []ToolResult        // tool calls made this turn
    Output      string              // final response
    LLMProfile  string              // which LLM profile to use
    Metadata    map[string]any      // stages can pass data forward
}

// ToolResult captures one tool call and its outcome.
type ToolResult struct {
    ToolName string
    Args     map[string]any
    Output   string
    Error    error
    Duration time.Duration
}

// Stage processes a turn. Returns error to abort the pipeline.
type Stage func(ctx context.Context, turn *Turn) error

// Pipeline is an ordered slice of stages.
type Pipeline []Stage

func (p Pipeline) Run(ctx context.Context, turn *Turn) error {
    for _, stage := range p {
        if err := stage(ctx, turn); err != nil {
            return err
        }
    }
    return nil
}
```

### 1.2 Pipeline Shapes

**Family chat:**
```
classify → policy_input → load_skills → filter_tools → llm_call → tool_loop(policy_tool_call) → policy_output → output_filter → compress_context
```

**CLI / power mode:**
```
load_skills → filter_tools → llm_call → tool_loop → compress_context
```

**Subagent (safe, for family mode):**
```
filter_tools → policy_tool_call → llm_call → tool_loop → policy_output
```

**Subagent (minimal, for CLI mode):**
```
filter_tools → llm_call → tool_loop
```

### 1.3 Package Layout

```
internal/agentcore/
    turn.go          # Turn, Stage, Pipeline types
    stage_classify.go
    stage_policy_input.go
    stage_policy_tool.go
    stage_policy_output.go
    stage_load_skills.go
    stage_filter_tools.go
    stage_llm_call.go
    stage_tool_loop.go
    stage_output_filter.go
    stage_compress.go
    stage_security_scan.go
    pipelines.go     # pre-built pipeline constructors (FamilyPipeline, CLIPipeline, etc.)
```

Each stage is a standalone function in its own file. Pipelines are assembled by constructors, not by the stages themselves.

### 1.4 Security Stages (OPA)

Three OPA checkpoints, all using the same evaluator with different Rego rules:

1. **`policy_input`** — evaluates user message before anything happens. Blocks, approves, or routes to parent approval. (Exists today.)
2. **`policy_tool_call`** — runs inside the tool loop before each tool executes. Evaluates: user role + tool name + tool arguments. Can block or route to parent approval.
3. **`policy_output`** — evaluates LLM response before returning to user. Replaces the current hardcoded `filterOutput()` blocked-pattern list with OPA rules.

Family mode includes all three. CLI mode skips all three.

---

## 2. Tool Calling

### 2.1 Tool Registry

Unified registry holding all tools with schemas, regardless of source:

```go
type Tool struct {
    Name        string             // namespaced: "mcp__weather__forecast"
    Description string
    InputSchema map[string]any     // JSON Schema for parameters
    Source      string             // "mcp", "builtin", "plugin"
    ServerName  string             // which MCP server owns this
    Roles       []string           // allowed roles (empty = all)
}

type Registry struct {
    tools map[string]*Tool
    mu    sync.RWMutex
}
```

Tools namespaced as `<source>__<server>__<name>` to prevent collisions. The existing `mcp.Pool` feeds tools into the registry at boot.

### 2.2 Schema Injection

The `llm_call` stage builds a `tools` array from `Turn.Tools` and includes it in the LLM request:

```json
{
  "model": "qwen3:4b",
  "messages": [...],
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "mcp__weather__forecast",
        "description": "Get weather forecast",
        "parameters": {
          "type": "object",
          "properties": {
            "location": {"type": "string"}
          },
          "required": ["location"]
        }
      }
    }
  ]
}
```

Both Ollama and llama.cpp OpenAI-compatible endpoints support this format. Cloud APIs (OpenAI, Anthropic) support it with minor translation in the LLM client.

### 2.3 Unified Tool Loop

Replace the current double-call pattern (streaming then non-streaming) with a single path:

```
if turn has tools:
    call LLM non-streaming with tools → response with possible tool_calls
    while response has tool_calls AND iterations < max:
        for each tool_call (parallel via errgroup):
            policy_tool_call → execute → collect result
        append results → call LLM again
    stream final text response to user
else:
    call LLM streaming (no tools) → stream tokens directly
```

Key changes:
- **One call, not two** — no more streaming-then-non-streaming double call
- **Parallel tool execution** — `errgroup` with concurrency limit (2 on RPi, higher elsewhere)
- **Streaming the final answer** — after tool loop resolves, the last LLM call can stream
- **Policy check per tool call** — OPA gate before every execution

### 2.4 Smart Tool Selection

Three strategies applied in order to keep tool schemas within context budget:

**Strategy 1: Static role filtering** (free, always applied)
Remove tools the user's role can't access. `under_8` might go from 40 tools to 8.

**Strategy 2: Skill-scoped tools** (free, always applied)
Skills declare which tools they need in frontmatter (`tools: [web_search, calculator]`). If only relevant skills are active this turn, only their declared tools are sent. Tools not claimed by any active skill are excluded.

**Strategy 3: Two-pass classification** (cheap LLM call, large tool sets only)
When tools still exceed budget after Strategy 1+2:
1. Send the LLM a **tool index** — names + one-line descriptions only (~5 tokens per tool)
2. LLM responds with which tools it needs
3. Inject full schemas only for selected tools

**Token budget:** Derived as a ratio of the auto-detected context window:
- 4K context → ~500 tool tokens (~2-3 schemas)
- 8K context → ~1500 tool tokens (~7-8 schemas)
- 32K+ context → ~8000 tool tokens (~40 schemas)
- 128K+ context → no limit

**Zero-tool fallback:** If Strategy 3's two-pass returns no tools selected, proceed without tool schemas (same as conversation-only mode). The LLM may still generate text that references tools conceptually, but won't produce `tool_calls` without schemas present.

**Conversation-only bypass:** When the classifier detects pure conversation ("how are you", "tell me a joke"), skip tool injection entirely.

---

## 3. Skills & Plugin Adapters

### 3.1 Internal Skill Representation

All external formats normalize to this:

```go
type Skill struct {
    Name        string
    Description string
    Version     string
    Author      string
    Tags        []string
    Tools       []string           // tools this skill needs (for smart selection)
    Trigger     SkillTrigger       // when to inject
    Body        string             // instructions for LLM context
    Format      string             // origin: "famclaw", "openclaw", "claudecode"
    Path        string
}

type SkillTrigger struct {
    Mode     string   // "always" | "keyword" | "classifier" | "manual"
    Keywords []string // for keyword mode
    Category string   // for classifier mode
}
```

### 3.2 Adapter Interface

```go
type SkillAdapter interface {
    Detect(path string) bool
    Parse(path string) (*Skill, error)
    FormatName() string
}
```

Three adapters at launch:

| Adapter | Detects | Parses |
|---|---|---|
| `FamClawAdapter` | `SKILL.md` with `name:` frontmatter | Current SKILL.md format |
| `OpenClawAdapter` | `SOUL.md` with `soul:` frontmatter | SOUL.md → maps `soul.tools`, `soul.triggers` |
| `ClaudeCodeAdapter` | `.md` with `description:` frontmatter, no `name:` | Claude Code agent markdown |

New ecosystems = one new file implementing `SkillAdapter`.

```go
var adapters = []SkillAdapter{
    &FamClawAdapter{},
    &OpenClawAdapter{},
    &ClaudeCodeAdapter{},
}

func DetectAndParse(path string) (*Skill, error) {
    for _, a := range adapters {
        if a.Detect(path) {
            return a.Parse(path)
        }
    }
    return nil, fmt.Errorf("no adapter recognized %q", path)
}
```

### 3.3 Deferred Skill Injection

The `load_skills` pipeline stage:

1. All installed skills have **metadata** loaded at boot (name, description, trigger, tools) — cheap, in memory
2. Each turn, evaluate triggers against the current message:
   - `"always"` → inject body
   - `"keyword"` → check keywords in user message
   - `"classifier"` → check category match
   - `"manual"` → only when user explicitly requests
3. Only matched skills get **full body** loaded and injected into system prompt
4. Matched skills' declared **tools** are added to the tool selection pool

Typical turn: 0-2 skills injected instead of all.

### 3.4 Plugin Install Command

```
famclaw plugin install <source>
```

| Source | Example | Behavior |
|---|---|---|
| Local path | `./my-skill/` | Auto-detect format via adapters |
| GitHub | `github.com/openclaw/skills/math-tutor` | Clone, detect, copy to registry |
| ClawHub | `clawhub://math-tutor` | Fetch from ClawHub API |
| MCP server | `--mcp github.com/someone/weather-mcp` | Add to MCP config directly |

All go through `DetectAndParse` → stored in `~/.famclaw/skills/<name>/` with original file plus a generated `manifest.json` caching parsed metadata.

---

## 4. Subagent Dispatching

### 4.1 Subagent Config

```go
type SubagentConfig struct {
    Prompt     string   // task description
    LLMProfile string   // explicit — parent decides
    Tools      []string // tool allowlist (empty = inherit parent's)
    DenyTools  []string // blocklist (applied after allowlist)
    MaxTurns   int      // tool loop iteration limit
    Pipeline   string   // "subagent" or "subagent_safe"
}
```

### 4.2 Pipeline Shapes

- **`subagent`** (CLI mode): `filter_tools → llm_call → tool_loop`
- **`subagent_safe`** (family mode): `filter_tools → policy_tool_call → llm_call → tool_loop → policy_output`

Family `Agent` always spawns `subagent_safe`. Parent's user context (role, age group) is inherited.

### 4.3 Scheduling

```go
type Scheduler struct {
    queue   chan *subagentJob
    workers int
}
```

- **Single backend** (one llama-server): `workers=1`, sequential queue. Parent yields LLM while waiting.
- **Multiple backends**: jobs targeting different LLM base URLs run in parallel. Same-backend jobs queue.
- Worker count auto-detected from number of distinct base URLs in configured LLM profiles.

### 4.4 Communication (Mailbox)

```go
type Mailbox struct {
    results   chan SubagentResult
    approvals chan ApprovalRequest
}

type SubagentResult struct {
    AgentID string
    Output  string
    Error   error
    Tools   []ToolResult
}

type ApprovalRequest struct {
    AgentID  string
    ToolName string
    Args     map[string]any
    Response chan bool
}
```

When `policy_tool_call` returns "request_approval" in a subagent, it sends an `ApprovalRequest` to the mailbox. Family mode routes this to the existing parent approval queue (notification → PIN → approve). Subagent blocks until approved or timed out.

### 4.5 spawn_agent Built-in Tool

The LLM requests a subagent via a built-in tool:

```json
{
  "name": "spawn_agent",
  "arguments": {
    "task": "Research photosynthesis and summarize for a 10-year-old",
    "tools": ["web_search"],
    "llm_profile": "cloud_claude"
  }
}
```

The tool:
1. Validates against user's role (can they use this profile? these tools?)
2. Builds `SubagentConfig`
3. Submits to scheduler
4. Returns result to parent's tool loop

Config controls which roles can spawn subagents and which LLM profiles are available per role.

---

## 5. Honeybadger Integration

[github.com/famclaw/honeybadger](https://github.com/famclaw/honeybadger) — security scanner for skills, tools, and MCP servers. Replaces the existing `internal/seccheck/` package.

### 5.1 Install-Time Gate

Before `famclaw plugin install` completes:

```
fetch repo → spawn honeybadger MCP subprocess → call honeybadger_scan →
  PASS → proceed with install
  WARN → install, notify parent, log to audit trail
  FAIL → block install, show findings, require parent override
```

Paranoia level from config. Defaults: `family` for child-installed plugins, `minimal` for parent-installed.

### 5.2 Runtime Pipeline Stage (`security_scan`)

Optional stage insertable in any pipeline:

```
classify → policy_input → security_scan → load_skills → filter_tools → ...
```

Triggers on:
- First use of a newly installed skill
- Subagent requesting tools from an unscanned external MCP server
- Tool call to a server not scanned in N days (configurable)

### 5.3 Audit Trail

Scan results stored in SQLite, visible in parent dashboard. Parents can see findings and revoke skill access.

### 5.4 Deprecation

`internal/seccheck/` is deprecated. Honeybadger subsumes it entirely via MCP.

---

## 6. Context Compression

### 6.1 Context Window Auto-Detection

On LLM profile connect, query the backend:

| Backend | Endpoint | Field |
|---|---|---|
| Ollama | `GET /api/show` | `model_info.context_length` |
| llama.cpp | `GET /props` | `default_generation_settings.n_ctx` |
| Cloud APIs | Lookup table | Known per model |

Cached per profile. Config override available: `context_window: 4096` to force a value.

All downstream logic (compression thresholds, tool budgets, skill injection limits) derives from the detected window as ratios, not absolute numbers.

### 6.2 Token Estimation

```go
type TokenEstimator interface {
    Estimate(text string) int
}

// SimpleEstimator: ~4 chars per token for English.
type SimpleEstimator struct{}

func (e *SimpleEstimator) Estimate(text string) int {
    return len(text) / 4
}
```

### 6.3 Three Compression Tiers

**Tier 0: Smart Truncation** (free)

Triggers at 70% of context window. Drops oldest turns but **never drops**:
- System prompt
- Tool call/result pairs from current session
- Last parent approval decision
- Messages flagged as "pinned" by skills

Replaces the current hard "last 20 messages" cutoff.

**Tier 1: Summary Compaction** (cheap LLM call)

When Tier 0 would lose important context (tool results, approvals):
- Call LLM: "Summarize this conversation in under 200 tokens. Preserve key facts, decisions, tool results."
- Replace dropped messages with a single summary system message
- Summary becomes the new floor — Tier 0 won't drop it

Cost: ~500 tokens in, ~200 out. A few seconds on local models.

**Tier 2: Full Recompression** (expensive, rare)

When Tier 1 summary + remaining messages still exceed budget:
- Summarize entire conversation including previous summary
- Reset to: system prompt + new summary + last 3 turns
- Log event to parent dashboard

### 6.4 Pipeline Stage

Runs **before** `llm_call` so the LLM always receives messages that fit its context window.

### 6.5 Per-User Persistence

```sql
CREATE TABLE conversation_summaries (
    conv_id      TEXT,
    summary      TEXT,
    covers_up_to INTEGER,
    created_at   DATETIME
);
```

Previous day's summary can seed the next day's conversation for cross-session continuity.

---

## Package Structure (New/Modified)

```
internal/
├── agentcore/           # NEW — pipeline engine
│   ├── turn.go
│   ├── stage_*.go       # one file per stage
│   └── pipelines.go     # pre-built pipeline constructors
├── toolreg/             # NEW — unified tool registry
│   ├── registry.go
│   └── tool.go
├── skilladapt/          # NEW — plugin adapter layer
│   ├── adapter.go       # interface + DetectAndParse
│   ├── famclaw.go       # SKILL.md adapter
│   ├── openclaw.go      # SOUL.md adapter
│   └── claudecode.go    # Claude Code agent .md adapter
├── subagent/            # NEW — subagent scheduling + mailbox
│   ├── scheduler.go
│   ├── mailbox.go
│   └── config.go
├── compress/            # NEW — context compression
│   ├── estimator.go
│   ├── truncate.go
│   ├── summarize.go
│   └── compress.go
├── agent/               # MODIFIED — becomes thin wrapper over agentcore
│   └── agent.go         # assembles FamilyPipeline, delegates to agentcore
├── mcp/                 # MODIFIED — feeds tools into toolreg.Registry
│   ├── client.go
│   └── pool.go
├── skillbridge/         # MODIFIED — uses skilladapt adapters internally
│   ├── skill.go
│   ├── registry.go
│   └── loader.go
├── seccheck/            # DEPRECATED — replaced by honeybadger MCP
└── llm/                 # MODIFIED — add tools field to requests, context window detection
    └── client.go
```

## Migration Path

1. `internal/agent/agent.go` keeps its public API (`NewAgent`, `Chat`) but internally delegates to `agentcore.Pipeline`
2. Existing tests continue to pass — `Agent.Chat()` behavior unchanged from the outside
3. `internal/seccheck/` marked deprecated, kept until honeybadger MCP is confirmed working
4. New features (subagents, CLI mode, smart tool selection) only available through the new pipeline
