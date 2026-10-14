package wasmskill

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/tetratelabs/wazero"
	wasi "github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// wasiModule is the only import namespace a skill module may use.
const wasiModule = "wasi_snapshot_preview1"

// Admission and run errors.
var (
	ErrInvalidModule    = errors.New("wasmskill: invalid wasm module")
	ErrNoStart          = errors.New("wasmskill: module does not export _start")
	ErrDisallowedImport = errors.New("wasmskill: module imports disallowed namespace")
)

// host is a wazero runtime with the WASI module pre-instantiated.
type host struct {
	rt wazero.Runtime
}

// newHost builds a runtime with WASI and the strict context policy: when
// the caller's context is canceled or expires, in-flight module calls are
// interrupted and the runtime is closed.
func newHost(ctx context.Context) (*host, error) {
	cfg := wazero.NewRuntimeConfig().WithCloseOnContextDone(true)
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi.Instantiate(ctx, rt); err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("wasmskill: instantiating wasi module: %w", err)
	}
	return &host{rt: rt}, nil
}

// admit checks a compiled module against the skill boundary before any
// instantiation: only WASI imports are allowed (no sockets, no clocks, no
// foreign host modules) and a _start export is required (a WASI command
// module).
func admit(compiled wazero.CompiledModule) error {
	for _, f := range compiled.ImportedFunctions() {
		if f.ModuleName() != wasiModule {
			return fmt.Errorf("%w: %s.%s", ErrDisallowedImport, f.ModuleName(), f.Name())
		}
	}
	for _, m := range compiled.ImportedMemories() {
		return fmt.Errorf("%w: imported memory from %q (index %d)", ErrDisallowedImport, m.ModuleName(), m.Index())
	}
	if _, ok := compiled.ExportedFunctions()["_start"]; !ok {
		return ErrNoStart
	}
	return nil
}

// RunResult is the captured I/O of one finished Run.
type RunResult struct {
	// Stdout is the guest's entire stdout (for a skill, its NDJSON lines).
	Stdout []byte
	// Stderr is the guest's entire stderr.
	Stderr []byte
	// ExitCode is the guest's exit code (0 for a clean return).
	ExitCode uint32
}

// Run runs one WASI command module to completion with bounded stdin,
// enforcing the skill boundary (admission, deny-by-default filesystem,
// WASI-only imports) and the caller's context. When ctx expires, the module
// is killed; the returned error is a *sys.ExitError (ExitCode 0xefffffff)
// wrapped with %w.
func Run(ctx context.Context, wasm []byte, cfg *Config, stdin []byte) (RunResult, error) {
	c, err := cfg.effective()
	if err != nil {
		return RunResult{}, err
	}
	h, err := newHost(ctx)
	if err != nil {
		return RunResult{}, err
	}
	defer h.rt.Close(context.Background())

	compiled, err := h.rt.CompileModule(ctx, wasm)
	if err != nil {
		return RunResult{}, fmt.Errorf("%w: %v", ErrInvalidModule, err)
	}
	if err := admit(compiled); err != nil {
		return RunResult{}, err
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var stderr bytes.Buffer
	mod, err := h.rt.InstantiateModule(ctx, compiled, moduleConfig(c, inR, outW, &stderr))
	if err != nil {
		return RunResult{}, fmt.Errorf("wasmskill: instantiating module %q: %w", c.Name, err)
	}

	stdoutCh := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(outR)
		stdoutCh <- b
	}()
	go func() {
		_, _ = inW.Write(stdin)
	}()

	start := mod.ExportedFunction("_start")
	_, callErr := start.Call(ctx)
	_ = outW.Close()

	res := RunResult{Stdout: <-stdoutCh, Stderr: stderr.Bytes()}
	var ex *sys.ExitError
	if errors.As(callErr, &ex) {
		res.ExitCode = ex.ExitCode()
	}
	if callErr != nil {
		return res, fmt.Errorf("wasmskill: running module %q: %w", c.Name, callErr)
	}
	return res, nil
}

// moduleConfig builds the per-module WASI configuration: the skill
// endpoint's stdio, the deny-by-default filesystem (exactly one preopen,
// mounted at "/", only when c.SandboxRoot is set), and the guest
// environment.
func moduleConfig(c Config, in io.Reader, out io.Writer, errw io.Writer) wazero.ModuleConfig {
	mcfg := wazero.NewModuleConfig().
		WithName(c.Name).
		WithStartFunctions().
		WithStdin(in).
		WithStdout(out).
		WithStderr(errw)
	if c.SandboxRoot != "" {
		mcfg = mcfg.WithFSConfig(wazero.NewFSConfig().WithDirMount(c.SandboxRoot, "/"))
	}
	for _, kv := range c.Env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			mcfg = mcfg.WithEnv(kv[:i], kv[i+1:])
		}
	}
	return mcfg
}
