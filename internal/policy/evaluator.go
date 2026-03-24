// Package policy provides an embedded OPA policy evaluator for FamClaw.
// Every message from every gateway is evaluated before the LLM sees it.
package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// NewEvaluator loads Rego policies from policyDir and data from dataDir.
func NewEvaluator(policyDir, dataDir string) (*Evaluator, error) {
	// Load all .rego files from policyDir
	modules := make(map[string]string)
	entries, err := os.ReadDir(policyDir)
	if err != nil {
		return nil, fmt.Errorf("reading policy dir %q: %w", policyDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".rego") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.rego") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(policyDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading policy file %q: %w", e.Name(), err)
		}
		modules[e.Name()] = string(raw)
	}

	if len(modules) == 0 {
		return nil, fmt.Errorf("no .rego files found in %q", policyDir)
	}

	// Load data files
	data := make(map[string]any)
	if dataDir != "" {
		dataEntries, err := os.ReadDir(dataDir)
		if err != nil {
			return nil, fmt.Errorf("reading data dir %q: %w", dataDir, err)
		}
		for _, e := range dataEntries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dataDir, e.Name()))
			if err != nil {
				return nil, fmt.Errorf("reading data file %q: %w", e.Name(), err)
			}
			var d map[string]any
			if err := json.Unmarshal(raw, &d); err != nil {
				return nil, fmt.Errorf("parsing data file %q: %w", e.Name(), err)
			}
			for k, v := range d {
				data[k] = v
			}
		}
	}

	store := inmem.NewFromObject(data)

	// Parse and compile modules
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

	// Prepare action query
	actionQ, err := rego.New(
		rego.Query("data.family.decision.action"),
		rego.Compiler(compiler),
		rego.Store(store),
	).PrepareForEval(context.Background())
	if err != nil {
		return nil, fmt.Errorf("preparing action query: %w", err)
	}

	// Prepare reason query
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

// Evaluate runs the policy against the given input and returns a Decision.
func (e *Evaluator) Evaluate(ctx context.Context, input Input) (Decision, error) {
	inputMap, err := toMap(input)
	if err != nil {
		return Decision{Action: "block", Reason: "Failed to marshal input"}, fmt.Errorf("marshaling input: %w", err)
	}

	// Evaluate action
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

	// Evaluate reason
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
