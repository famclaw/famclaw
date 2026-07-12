package wasmskill

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Runtime manages a WASI-based WebAssembly module execution with
// sandboxed filesystem access and stdio communication.
type Runtime struct {
	wasmModule []byte
	config     Config
	runtime    wazero.Runtime
	module     api.Module
	closeFunc  func(context.Context) error
	mu         sync.Mutex
}

// Config holds configuration for the WASM skill runtime.
type Config struct {
	// SandboxRoot is the directory that the WASM module can access.
	// All filesystem operations are restricted to this directory.
	SandboxRoot string
	// Stdin is the input stream for the WASM module.
	// If nil, os.Stdin is used.
	Stdin io.Reader
	// Stdout is the output stream for the WASM module.
	// If nil, os.Stdout is used.
	Stdout io.Writer
	// Stderr is the error stream for the WASM module.
	// If nil, os.Stderr is used.
	Stderr io.Writer
	// Args are the arguments passed to the WASM module.
	Args []string
	// Env are the environment variables passed to the WASM module.
	Env []string
}

// NewRuntime creates a new WASM skill runtime with the given WASM bytecode and config.
func NewRuntime(ctx context.Context, wasmModule []byte, config *Config) (*Runtime, error) {
	if config == nil {
		config = &Config{}
	}

	// Enforce non-empty SandboxRoot for security (deny-by-default filesystem access)
	if config.SandboxRoot == "" {
		return nil, fmt.Errorf("SandboxRoot must be set (deny-by-default filesystem access)")
	}

	// Set default stdio streams if not provided
	if config.Stdin == nil {
		config.Stdin = os.Stdin
	}
	if config.Stdout == nil {
		config.Stdout = os.Stdout
	}
	if config.Stderr == nil {
		config.Stderr = os.Stderr
	}

	// Validate Env has even number of elements (key-value pairs)
	if len(config.Env)%2 != 0 {
		return nil, fmt.Errorf("Env must contain key-value pairs (even number of elements), got %d", len(config.Env))
	}

	r := &Runtime{
		wasmModule: wasmModule,
		config:     *config,
	}

	// Create wazero runtime
	runtime := wazero.NewRuntime(ctx)

	// Instantiate WASI (this makes WASI functions available to modules)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("instantiating WASI: %w", err)
	}

	// Compile the module first to validate imports
	compiledModule, err := runtime.CompileModule(ctx, wasmModule)
	if err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("compiling module: %w", err)
	}

	// Validate that the module only imports from WASI (deny-by-default imports)
	if err := validateImports(compiledModule); err != nil {
		compiledModule.Close(ctx)
		runtime.Close(ctx)
		return nil, fmt.Errorf("module import validation failed: %w", err)
	}

	// Create module configuration
	moduleConfig := wazero.NewModuleConfig().
		// Configure stdio
		WithStdin(config.Stdin).
		WithStdout(config.Stdout).
		WithStderr(config.Stderr).
		// Configure arguments
		WithArgs(config.Args...)

	// Configure environment variables (WithEnv takes key-value pairs)
	for i := 0; i < len(config.Env); i += 2 {
		moduleConfig = moduleConfig.WithEnv(config.Env[i], config.Env[i+1])
	}

	// Add the sandbox directory as a preopened directory
	// This is the ONLY directory the WASM module can access
	moduleConfig = moduleConfig.WithFSConfig(
		wazero.NewFSConfig().WithDirMount(config.SandboxRoot, "/"),
	)

	// Instantiate the module with the module configuration
	module, err := runtime.InstantiateModule(ctx, compiledModule, moduleConfig)
	compiledModule.Close(ctx)
	if err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("instantiating module: %w", err)
	}

	r.runtime = runtime
	r.module = module

	// Set up close function
	r.closeFunc = func(ctx context.Context) error {
		moduleErr := r.module.Close(ctx)
		runtimeErr := r.runtime.Close(ctx)
		return errors.Join(moduleErr, runtimeErr)
	}

	return r, nil
}

// Close closes the runtime and releases all resources.
func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closeFunc != nil {
		return r.closeFunc(ctx)
	}
	return nil
}

// Execute runs the WASM module with the assumption that it follows
// the WASI command pattern: reads JSON-RPC from stdin, writes to stdout.
// This is suitable for MCP-style skills that communicate via stdio.
func (r *Runtime) Execute(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// For a WASI command that communicates via stdio, we just need to
	// start the module and let it run until completion.
	// The module should read from WASI fd 0 (stdin) and write to fd 1 (stdout)

	// Find the _start function (standard WASI entry point)
	startFunc := r.module.ExportedFunction("_start")
	if startFunc == nil {
		// If no _start function, look for main or other entry points
		// For now, we'll require _start as it's the standard WASI command entry point
		return fmt.Errorf("module missing _start function: WASI command modules must export _start as the entry point (required for stdio-based communication)")
	}

	// Execute the module
	_, err := startFunc.Call(ctx)
	return err
}

// ErrNoStartFunction is returned when the WASM module doesn't have a _start function.
var ErrNoStartFunction = NewError("module missing _start function (required for WASI commands)")

// Error is a wrapper for wasmskill-specific errors.
type Error struct {
	msg string
}

// NewError creates a new wasmskill Error.
func NewError(msg string) Error {
	return Error{msg: msg}
}

func (e Error) Error() string {
	return e.msg
}

// TODO: Implement the exported function ABI (skill_init/invoke/cleanup)
// as described in the design document for a future phase.
// For now, we focus on the stdio-based MCP-style communication path.

// validateImports ensures the module only imports from allowed namespaces (WASI).
func validateImports(module wazero.CompiledModule) error {
	imports := module.ImportedFunctions()
	for _, imp := range imports {
		// Check if this is an import (not an export)
		if modName, _, isImport := imp.Import(); isImport {
			// Only allow imports from WASI snapshot preview1
			if modName != "wasi_snapshot_preview1" {
				return fmt.Errorf("module imports disallowed namespace %q (only wasi_snapshot_preview1 allowed)", modName)
			}
		}
	}
	return nil
}
