// Package policy provides an embedded OPA policy evaluator for FamClaw.
// Every message from every gateway is evaluated before the LLM sees it.
package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/open-policy-agent/opa/ast"
	"github.com/open-policy-agent/opa/rego"
	"github.com/open-policy-agent/opa/storage"
	"github.com/open-policy-agent/opa/storage/inmem"
)

// Evaluator wraps an embedded OPA instance for policy decisions.
type Evaluator struct {
	actionQuery rego.PreparedEvalQuery
	reasonQuery rego.PreparedEvalQuery
	store       storage.Store
}

// NewEvaluator builds an evaluator. When policyDir/dataDir are empty,
// the built-in policies compiled into the binary are loaded; otherwise
// they are read from the given filesystem paths.
func NewEvaluator(policyDir, dataDir string) (*Evaluator, error) {
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

	return &Evaluator{actionQuery: actionQ, reasonQuery: reasonQ, store: store}, nil
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

func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	err = json.Unmarshal(b, &m)
	return m, err
}
