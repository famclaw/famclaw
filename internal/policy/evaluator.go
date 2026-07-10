// Package policy provides an embedded OPA policy evaluator for FamClaw.
// Every message from every gateway is evaluated before the LLM sees it.
package policy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/open-policy-agent/opa/ast"
	"github.com/open-policy-agent/opa/rego"
	"github.com/open-policy-agent/opa/storage"
	"github.com/open-policy-agent/opa/storage/inmem"
)

// Evaluator wraps an embedded OPA instance for policy decisions.
type Evaluator struct {
	actionQuery       rego.PreparedEvalQuery
	reasonQuery       rego.PreparedEvalQuery
	toolAllowQuery    rego.PreparedEvalQuery
	toolReasonQuery   rego.PreparedEvalQuery
	outputAllowQuery  rego.PreparedEvalQuery
	outputReasonQuery rego.PreparedEvalQuery
	outputRedactQuery rego.PreparedEvalQuery
	skillAllowQuery   rego.PreparedEvalQuery
	skillReasonQuery  rego.PreparedEvalQuery
	store             storage.Store
}

// NewEvaluator builds an evaluator. When policyDir/dataDir are empty,
// the built-in policies compiled into the binary are loaded; otherwise
// they are read from the given filesystem paths.
// The optional expectedHash parameter performs an integrity check on the
// loaded policy set; pass "" to skip verification.
func NewEvaluator(policyDir, dataDir, expectedHash string) (*Evaluator, error) {
	if (policyDir == "") != (dataDir == "") {
		return nil, fmt.Errorf("policyDir and dataDir must both be set or both be empty (got policyDir=%q, dataDir=%q)", policyDir, dataDir)
	}

	modules := make(map[string]string)
	data := make(map[string]any)

	policyFS, policyRoot := policySource(policyDir)
	if err := loadModules(policyFS, policyRoot, modules); err != nil {
		return nil, err
	}
	if len(modules) == 0 {
		return nil, fmt.Errorf("no .rego files found")
	}

	dataFS, dataRoot := dataSource(dataDir)
	if err := loadData(dataFS, dataRoot, data); err != nil {
		return nil, err
	}

	// ── Policy integrity verification ─────────────────────────────────
	if hash, err := computePolicyHash(policyFS, policyRoot); err != nil {
		return nil, fmt.Errorf("computing policy hash: %w", err)
	} else if expectedHash != "" {
		if hash != expectedHash {
			return nil, fmt.Errorf(
				"policy hash mismatch: expected=%q computed=%q — policy files may have been tampered with",
				expectedHash, hash,
			)
		}
		log.Printf("policy_hash=%s verified", hash)
	} else {
		log.Printf("policy_hash=%s (set policies.expected_hash to pin)", hash)
	}

	// NOTE: the hash is computed directly from the filesystem to ensure
	// it reflects the exact bytes on disk, independent of the in-memory
	// modules map used for parsing below.

	store := inmem.NewFromObject(data)

	parsedModules := make(map[string]*ast.Module)
	for name, src := range modules {
		mod, err := ast.ParseModuleWithOpts(name, src, ast.ParserOptions{ProcessAnnotation: true})
		if err != nil {
			return nil, fmt.Errorf("parsing rego module %q: %w", name, err)
		}
		parsedModules[name] = mod
	}

	compiler := ast.NewCompiler()
	compiler.Compile(parsedModules)
	if compiler.Failed() {
		return nil, fmt.Errorf("compiling rego: %v", compiler.Errors)
	}

	actionQ, err := rego.New(
		rego.Query("data.family.decision.action"),
		rego.Compiler(compiler),
		rego.Store(store),
	).PrepareForEval(context.Background())
	if err != nil {
		return nil, fmt.Errorf("preparing action query: %w", err)
	}

	reasonQ, err := rego.New(
		rego.Query("data.family.decision.reason"),
		rego.Compiler(compiler),
		rego.Store(store),
	).PrepareForEval(context.Background())
	if err != nil {
		return nil, fmt.Errorf("preparing reason query: %w", err)
	}

	toolAllowQ, err := rego.New(
		rego.Query("data.family.tool_policy.allow"),
		rego.Compiler(compiler),
		rego.Store(store),
	).PrepareForEval(context.Background())
	if err != nil {
		return nil, fmt.Errorf("preparing tool_policy allow query: %w", err)
	}

	toolReasonQ, err := rego.New(
		rego.Query("data.family.tool_policy.reason"),
		rego.Compiler(compiler),
		rego.Store(store),
	).PrepareForEval(context.Background())
	if err != nil {
		return nil, fmt.Errorf("preparing tool_policy reason query: %w", err)
	}

	outputAllowQ, err := rego.New(
		rego.Query("data.family.output_policy.allow_output"),
		rego.Compiler(compiler),
		rego.Store(store),
	).PrepareForEval(context.Background())
	if err != nil {
		return nil, fmt.Errorf("preparing output_policy allow_output query: %w", err)
	}

	outputReasonQ, err := rego.New(
		rego.Query("data.family.output_policy.reason"),
		rego.Compiler(compiler),
		rego.Store(store),
	).PrepareForEval(context.Background())
	if err != nil {
		return nil, fmt.Errorf("preparing output_policy reason query: %w", err)
	}

	outputRedactQ, err := rego.New(
		rego.Query("data.family.output_policy.redact"),
		rego.Compiler(compiler),
		rego.Store(store),
	).PrepareForEval(context.Background())
	if err != nil {
		return nil, fmt.Errorf("preparing output_policy redact query: %w", err)
	}

	skillAllowQ, err := rego.New(
		rego.Query("data.family.skill_prompt_policy.allow"),
		rego.Compiler(compiler),
		rego.Store(store),
	).PrepareForEval(context.Background())
	if err != nil {
		return nil, fmt.Errorf("preparing skill_prompt_policy allow query: %w", err)
	}

	skillReasonQ, err := rego.New(
		rego.Query("data.family.skill_prompt_policy.reason"),
		rego.Compiler(compiler),
		rego.Store(store),
	).PrepareForEval(context.Background())
	if err != nil {
		return nil, fmt.Errorf("preparing skill_prompt_policy reason query: %w", err)
	}

	return &Evaluator{
		actionQuery:       actionQ,
		reasonQuery:       reasonQ,
		toolAllowQuery:    toolAllowQ,
		toolReasonQuery:   toolReasonQ,
		outputAllowQuery:  outputAllowQ,
		outputReasonQuery: outputReasonQ,
		outputRedactQuery: outputRedactQ,
		skillAllowQuery:   skillAllowQ,
		skillReasonQuery:  skillReasonQ,
		store:             store,
	}, nil
}

// policySource returns the filesystem and root directory to load policy
// modules from. An empty dir means "use embedded".
func policySource(dir string) (fs.FS, string) {
	if dir == "" {
		return embeddedPolicies, "policies/family"
	}
	return os.DirFS(dir), "."
}

// dataSource returns the filesystem and root directory to load data
// files from. An empty dir means "use embedded".
func dataSource(dir string) (fs.FS, string) {
	if dir == "" {
		return embeddedData, "policies/data"
	}
	return os.DirFS(dir), "."
}

func loadModules(fsys fs.FS, dir string, modules map[string]string) error {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return fmt.Errorf("reading policy dir %q: %w", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".rego") || strings.HasSuffix(name, "_test.rego") {
			continue
		}
		raw, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return fmt.Errorf("reading policy file %q: %w", name, err)
		}
		modules[name] = string(raw)
	}
	return nil
}

// ComputeEmbeddedPolicyHash returns the SHA-256 hex digest of the
// embedded OPA policy files. It is exported for CLI tooling.
func ComputeEmbeddedPolicyHash() (string, error) {
	return computePolicyHash(embeddedPolicies, "policies/family")
}

// computePolicyHash returns a deterministic SHA-256 hex digest of all
// .rego files (excluding *_test.rego) in the policy directory. Files are
// sorted by name; each file's raw bytes are hashed sequentially into a
// running SHA-256. The final digest is returned as a lowercase hex string.
func computePolicyHash(fsys fs.FS, dir string) (string, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return "", fmt.Errorf("reading policy dir %q: %w", dir, err)
	}

	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".rego") || strings.HasSuffix(name, "_test.rego") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	h := sha256.New()
	for _, name := range names {
		raw, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return "", fmt.Errorf("reading policy file %q: %w", name, err)
		}
		h.Write(raw)
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func loadData(fsys fs.FS, dir string, data map[string]any) error {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return fmt.Errorf("reading data dir %q: %w", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return fmt.Errorf("reading data file %q: %w", name, err)
		}
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			return fmt.Errorf("parsing data file %q: %w", name, err)
		}
		// Top-level keys merge into data; OPA rules see e.g.
		// `data.categories[...]`, not `data.topics.categories[...]`.
		for k, v := range d {
			data[k] = v
		}
	}
	return nil
}

// Evaluate runs the policy against the given input and returns a Decision.
func (e *Evaluator) Evaluate(ctx context.Context, input Input) (Decision, error) {
	inputMap, err := toMap(input)
	if err != nil {
		return Decision{Action: "block", Reason: "Failed to marshal input"}, fmt.Errorf("marshaling input: %w", err)
	}

	actionResults, err := e.actionQuery.Eval(ctx, rego.EvalInput(inputMap))
	if err != nil {
		return Decision{Action: "block", Reason: "Policy evaluation error"}, fmt.Errorf("evaluating action: %w", err)
	}

	action := "block"
	if len(actionResults) > 0 && len(actionResults[0].Expressions) > 0 {
		if a, ok := actionResults[0].Expressions[0].Value.(string); ok {
			action = a
		}
	}

	reason := ""
	reasonResults, err := e.reasonQuery.Eval(ctx, rego.EvalInput(inputMap))
	if err == nil && len(reasonResults) > 0 && len(reasonResults[0].Expressions) > 0 {
		if r, ok := reasonResults[0].Expressions[0].Value.(string); ok {
			reason = r
		}
	}

	return Decision{Action: action, Reason: reason}, nil
}

// EvaluateToolCall returns the tool-call policy decision for the given user
// and tool name. Fail-closed: returns ToolDecision{Allow: false} when OPA
// returns no result — matches the `default allow := false` rule in
// tool_policy.rego after the default-DENY migration. Evaluation errors are
// propagated and treated as deny by callers.
func (e *Evaluator) EvaluateToolCall(ctx context.Context, input ToolCallInput) (ToolDecision, error) {
	inputMap, err := toMap(input)
	if err != nil {
		return ToolDecision{}, fmt.Errorf("marshaling tool input: %w", err)
	}

	allowResults, err := e.toolAllowQuery.Eval(ctx, rego.EvalInput(inputMap))
	if err != nil {
		return ToolDecision{}, fmt.Errorf("evaluating tool_policy allow: %w", err)
	}

	allow := false // fail-closed: if OPA returns no result, deny
	if len(allowResults) > 0 && len(allowResults[0].Expressions) > 0 {
		if v, ok := allowResults[0].Expressions[0].Value.(bool); ok {
			allow = v
		}
	}

	reason := ""
	reasonResults, err := e.toolReasonQuery.Eval(ctx, rego.EvalInput(inputMap))
	if err == nil && len(reasonResults) > 0 && len(reasonResults[0].Expressions) > 0 {
		if r, ok := reasonResults[0].Expressions[0].Value.(string); ok {
			reason = r
		}
	}

	return ToolDecision{Allow: allow, Reason: reason}, nil
}

// EvaluateOutput checks the LLM draft response against the output policy.
// Fail-closed: returns OutputDecision{Allow: false} on evaluation error.
func (e *Evaluator) EvaluateOutput(ctx context.Context, input OutputInput) (OutputDecision, error) {
	m, err := toMap(input)
	if err != nil {
		return OutputDecision{Allow: false}, fmt.Errorf("marshalling output input: %w", err)
	}

	allow := false // fail-closed
	rs, err := e.outputAllowQuery.Eval(ctx, rego.EvalInput(m))
	if err != nil {
		return OutputDecision{Allow: false}, fmt.Errorf("evaluating output allow: %w", err)
	}
	if len(rs) > 0 && len(rs[0].Expressions) > 0 {
		if v, ok := rs[0].Expressions[0].Value.(bool); ok {
			allow = v
		}
	}

	reason := ""
	rs2, err := e.outputReasonQuery.Eval(ctx, rego.EvalInput(m))
	if err == nil && len(rs2) > 0 && len(rs2[0].Expressions) > 0 {
		reason, _ = rs2[0].Expressions[0].Value.(string)
	}

	var redact []string
	rs3, err := e.outputRedactQuery.Eval(ctx, rego.EvalInput(m))
	if err == nil && len(rs3) > 0 && len(rs3[0].Expressions) > 0 {
		if arr, ok := rs3[0].Expressions[0].Value.([]interface{}); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					redact = append(redact, s)
				}
			}
		}
	}

	return OutputDecision{Allow: allow, Reason: reason, Redact: redact}, nil
}

// EvaluateSkillPrompt checks a skill's prompt body against the skill-prompt policy.
// Fail-closed: returns SkillPromptDecision{Allow: false} on evaluation error.
func (e *Evaluator) EvaluateSkillPrompt(ctx context.Context, input SkillPromptInput) (SkillPromptDecision, error) {
	m, err := toMap(input)
	if err != nil {
		return SkillPromptDecision{Allow: false}, fmt.Errorf("marshalling skill prompt input: %w", err)
	}

	allow := false // fail-closed
	rs, err := e.skillAllowQuery.Eval(ctx, rego.EvalInput(m))
	if err != nil {
		return SkillPromptDecision{Allow: false}, fmt.Errorf("evaluating skill allow: %w", err)
	}
	if len(rs) > 0 && len(rs[0].Expressions) > 0 {
		if v, ok := rs[0].Expressions[0].Value.(bool); ok {
			allow = v
		}
	}

	reason := ""
	rs2, err := e.skillReasonQuery.Eval(ctx, rego.EvalInput(m))
	if err == nil && len(rs2) > 0 && len(rs2[0].Expressions) > 0 {
		reason, _ = rs2[0].Expressions[0].Value.(string)
	}

	return SkillPromptDecision{Allow: allow, Reason: reason}, nil
}

func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	err = json.Unmarshal(b, &m)
	return m, err
}
