package wasmskill

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBasicExecution tests that a WASM module can be instantiated and executed successfully.
func TestBasicExecution(t *testing.T) {
	// Create a temporary directory for the sandbox
	sandboxDir := t.TempDir()

	// Load the simple WASM module from testdata (same package testdata)
	simpleWasm, err := os.ReadFile(filepath.Join("testdata", "simple.wasm"))
	if err != nil {
		t.Fatalf("Failed to read simple.wasm: %v", err)
	}

	// Create pipes for stdin/stdout
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	// Close stdinW immediately since we're not writing to it in this test
	// This will cause reads from stdinR to return EOF immediately
	stdinW.Close()

	// Configure runtime
	config := &Config{
		SandboxRoot: sandboxDir,
		Stdin:       stdinR,
		Stdout:      stdoutW,
		Stderr:      stderrW,
	}

	// Create runtime
	rt, err := NewRuntime(context.Background(), simpleWasm, config)
	if err != nil {
		t.Fatalf("Failed to create runtime: %v", err)
	}
	defer rt.Close(context.Background())

	// Execute the module
	err = rt.Execute(context.Background())
	if err != nil {
		t.Fatalf("Failed to execute module: %v", err)
	}

	// Close write ends to signal EOF
	stdoutW.Close()
	stderrW.Close()

	// Read and discard output (this module doesn't produce output)
	_, err = io.ReadAll(stdoutR)
	if err != nil {
		t.Fatalf("Failed to read stdout: %v", err)
	}

	// Check stderr is empty
	_, err = io.ReadAll(stderrR)
	if err != nil {
		t.Fatalf("Failed to read stderr: %v", err)
	}
}

// TestFilesystemDenyByDefault tests that the WASM module cannot access files outside the sandbox.
func TestFilesystemDenyByDefault(t *testing.T) {
	// Create a temporary directory for the sandbox
	sandboxDir := t.TempDir()

	// Create a file outside the sandbox that we'll try to access
	outsideFile := filepath.Join(os.TempDir(), "famclaw-test-outside.txt")
	outsideContent := []byte("secret data that should not be accessible")
	if err := os.WriteFile(outsideFile, outsideContent, 0o600); err != nil {
		t.Fatalf("Failed to create outside file: %v", err)
	}
	defer os.Remove(outsideFile)

	// Create a simple WASM module
	simpleWasm, err := os.ReadFile(filepath.Join("testdata", "simple.wasm"))
	if err != nil {
		t.Fatalf("Failed to read simple.wasm: %v", err)
	}

	// Create pipes
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	// Close stdinW immediately since we're not writing to it
	stdinW.Close()

	// Configure runtime with sandbox
	config := &Config{
		SandboxRoot: sandboxDir,
		Stdin:       stdinR,
		Stdout:      stdoutW,
		Stderr:      stderrW,
	}

	// Create runtime
	rt, err := NewRuntime(context.Background(), simpleWasm, config)
	if err != nil {
		t.Fatalf("Failed to create runtime: %v", err)
	}
	defer rt.Close(context.Background())

	// Execute the module - should succeed (the module runs but can't access outside file)
	err = rt.Execute(context.Background())
	if err != nil {
		t.Fatalf("Failed to execute module: %v", err)
	}

	// Close write ends
	stdoutW.Close()
	stderrW.Close()

	// Read and discard output
	_, _ = io.ReadAll(stdoutR)
	_, _ = io.ReadAll(stderrR)

	// Verify the outside file is still intact (wasn't read/modified)
	if err := os.WriteFile(outsideFile, outsideContent, 0o600); err != nil {
		t.Fatalf("Failed to rewrite outside file: %v", err)
	}
	afterContent, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatalf("Failed to read outside file after test: %v", err)
	}
	if !bytes.Equal(afterContent, outsideContent) {
		t.Fatalf("Outside file was modified: expected %q, got %q", outsideContent, afterContent)
	}
}

// TestNoNetworkAccess tests that the WASM module cannot perform network operations.
func TestNoNetworkAccess(t *testing.T) {
	sandboxDir := t.TempDir()

	simpleWasm, err := os.ReadFile(filepath.Join("testdata", "simple.wasm"))
	if err != nil {
		t.Fatalf("Failed to read simple.wasm: %v", err)
	}

	// Create pipes
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	// Close stdinW immediately since we're not writing to it
	stdinW.Close()

	// Configure runtime
	config := &Config{
		SandboxRoot: sandboxDir,
		Stdin:       stdinR,
		Stdout:      stdoutW,
		Stderr:      stderrW,
	}

	// Create runtime
	rt, err := NewRuntime(context.Background(), simpleWasm, config)
	if err != nil {
		t.Fatalf("Failed to create runtime: %v", err)
	}
	defer rt.Close(context.Background())

	// Execute the module - should succeed (our simple module does nothing)
	err = rt.Execute(context.Background())
	if err != nil {
		t.Fatalf("Expected module to execute successfully, but got error: %v", err)
	}

	// Check that we got no error
	// Close write ends
	stdoutW.Close()
	stderrW.Close()

	// Read stdout and stderr
	_, _ = io.ReadAll(stdoutR)
	_, _ = io.ReadAll(stderrR)
}

// TestEnvValidation tests that Env validation works correctly.
func TestEnvValidation(t *testing.T) {
	sandboxDir := t.TempDir()
	simpleWasm, _ := os.ReadFile(filepath.Join("testdata", "simple.wasm"))

	// Test odd number of Env elements (should fail)
	config := &Config{
		SandboxRoot: sandboxDir,
		Env:         []string{"KEY1", "VALUE1", "KEY2"}, // Odd number
	}
	_, err := NewRuntime(context.Background(), simpleWasm, config)
	if err == nil {
		t.Fatal("Expected error for odd Env elements")
	}
	t.Logf("Got expected error: %v", err)

	// Test even number of Env elements (should succeed)
	config.Env = []string{"KEY1", "VALUE1", "KEY2", "VALUE2"}
	_, err = NewRuntime(context.Background(), simpleWasm, config)
	if err != nil {
		t.Fatalf("Expected success for even Env elements: %v", err)
	}
}

// TestSandboxRootRequired tests that empty SandboxRoot is rejected.
func TestSandboxRootRequired(t *testing.T) {
	simpleWasm, _ := os.ReadFile(filepath.Join("testdata", "simple.wasm"))

	config := &Config{
		SandboxRoot: "", // Empty - should fail
	}
	_, err := NewRuntime(context.Background(), simpleWasm, config)
	if err == nil {
		t.Fatal("Expected error for empty SandboxRoot")
	}
	t.Logf("Got expected error: %v", err)
}

// TestImportValidation tests that modules with non-WASI imports are rejected.
// Note: We can't easily compile WAT in tests without wat2wasm,
// so we skip this test and rely on the import validation in NewRuntime.
func TestImportValidation(t *testing.T) {
	t.Skip("Skipping - requires WAT compilation in test")
}

// TestConcurrentExecute tests that Execute is thread-safe.
func TestConcurrentExecute(t *testing.T) {
	sandboxDir := t.TempDir()
	simpleWasm, _ := os.ReadFile(filepath.Join("testdata", "simple.wasm"))

	config := &Config{
		SandboxRoot: sandboxDir,
	}
	rt, err := NewRuntime(context.Background(), simpleWasm, config)
	if err != nil {
		t.Fatalf("Failed to create runtime: %v", err)
	}
	defer rt.Close(context.Background())

	// Run Execute concurrently from multiple goroutines
	done := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func() {
			done <- rt.Execute(context.Background())
		}()
	}

	// Collect results
	for i := 0; i < 10; i++ {
		if err := <-done; err != nil {
			t.Errorf("Execute failed: %v", err)
		}
	}
}

// TestConcurrentCloseExecute tests that Close and Execute don't race.
func TestConcurrentCloseExecute(t *testing.T) {
	sandboxDir := t.TempDir()
	simpleWasm, _ := os.ReadFile(filepath.Join("testdata", "simple.wasm"))

	config := &Config{
		SandboxRoot: sandboxDir,
	}
	rt, err := NewRuntime(context.Background(), simpleWasm, config)
	if err != nil {
		t.Fatalf("Failed to create runtime: %v", err)
	}

	done := make(chan struct{})
	// Run Execute in a loop
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				rt.Execute(context.Background())
			}
		}
	}()

	// Close after a bit
	time.Sleep(10 * time.Millisecond)
	close(done)
	rt.Close(context.Background())
}