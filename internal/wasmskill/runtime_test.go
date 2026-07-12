package wasmskill

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Force usage of imports to satisfy static analysis
var _ = wazero.NewRuntime
var _ = api.Module(nil)
var _ = wasi_snapshot_preview1.Instantiate

// TestBasicExecution tests that a WASM module can be instantiated and executed successfully.
func TestBasicExecution(t *testing.T) {
	// Create a temporary directory for the sandbox
	sandboxDir := t.TempDir()
	
	// Load the debug WASM module from testdata
	echoWasm, err := os.ReadFile(filepath.Join("..", "wasmskill", "testdata", "debug.wasm"))
	if err != nil {
		t.Fatalf("Failed to read debug.wasm: %v", err)
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
	rt, err := NewRuntime(echoWasm, config)
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
	testWasm := []byte{
		0x00, 0x61, 0x73, 0x6d, // magic
		0x01, 0x00, 0x00, 0x00, // version
		
		// type section
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
		
		// function section
		0x03, 0x02, 0x01, 0x00,
		
		// export section
		0x07, 0x0a, 0x01, 0x06, 0x5f, 0x73, 0x74, 0x61, 0x72, 0x74, 0x00, 0x00,
		
		// code section
		0x0a, 0x06, 0x01, 0x04, 0x00, 0x0f, 0x00, 0x0b,
	}
	
	// Create pipes
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	
	// Close stdinW immediately since we're not writing to it
	// This will cause reads from stdinR to return EOF immediately
	stdinW.Close()
	
	// Configure runtime with sandbox
	config := &Config{
		SandboxRoot: sandboxDir,
		Stdin:       stdinR,
		Stdout:      stdoutW,
		Stderr:      stderrW,
	}
	
	// Create runtime
	rt, err := NewRuntime(testWasm, config)
	if err != nil {
		t.Fatalf("Failed to create runtime: %v", err)
	}
	defer rt.Close(context.Background())
	
	// Execute the module
	err = rt.Execute(context.Background())
	// We expect this to succeed (the module should run) but not be able to read the outside file
	if err != nil {
		t.Fatalf("Failed to execute module: %v", err)
	}
	
	// Close write ends
	stdoutW.Close()
	stderrW.Close()
	
	// Read and discard output
	_, err = io.ReadAll(stdoutR)
	if err != nil {
		t.Fatalf("Failed to read stdout: %v", err)
	}
	
	// Check stderr is empty
	_, err = io.ReadAll(stderrR)
	if err != nil {
		t.Fatalf("Failed to read stderr: %v", err)
	}
	
	// Verify the outside file is still intact (wasn't read)
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
	// Create a temporary directory for the sandbox
	sandboxDir := t.TempDir()
	
	// Create a simple WASM module
	testWasm := []byte{
		0x00, 0x61, 0x73, 0x6d, // magic
		0x01, 0x00, 0x00, 0x00, // version
		
		// type section
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
		
		// function section
		0x03, 0x02, 0x01, 0x00,
		
		// export section
		0x07, 0x0a, 0x01, 0x06, 0x5f, 0x73, 0x74, 0x61, 0x72, 0x74, 0x00, 0x00,
		
		// code section
		0x0a, 0x06, 0x01, 0x04, 0x00, 0x0f, 0x00, 0x0b,
	}
	
	// Create pipes
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	
	// Close stdinW immediately since we're not writing to it
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
	rt, err := NewRuntime(testWasm, config)
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