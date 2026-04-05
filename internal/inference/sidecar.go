// Package inference manages local LLM inference via llama-server (llama.cpp).
// FamClaw spawns and manages the llama-server process as a sidecar,
// providing OpenAI-compatible API on a local port.
package inference

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Sidecar manages a llama-server subprocess.
type Sidecar struct {
	binaryPath string
	modelPath  string
	port       int
	gpuLayers  int
	extraArgs  []string // e.g. --cache-type-k q4_0 for TurboQuant

	cmd     *exec.Cmd
	mu      sync.Mutex
	running bool
}

// SidecarConfig configures the llama-server sidecar.
type SidecarConfig struct {
	BinaryPath string   // path to llama-server binary
	ModelPath  string   // path to GGUF model file
	Port       int      // HTTP port (default 8081)
	GPULayers  int      // layers to offload to GPU (0 = CPU only)
	ExtraArgs  []string // additional args (e.g. TurboQuant flags)
}

// NewSidecar creates a new sidecar manager.
func NewSidecar(cfg SidecarConfig) *Sidecar {
	if cfg.Port == 0 {
		cfg.Port = 8081
	}
	return &Sidecar{
		binaryPath: cfg.BinaryPath,
		modelPath:  cfg.ModelPath,
		port:       cfg.Port,
		gpuLayers:  cfg.GPULayers,
		extraArgs:  cfg.ExtraArgs,
	}
}

// BaseURL returns the OpenAI-compatible base URL for the sidecar.
func (s *Sidecar) BaseURL() string {
	return fmt.Sprintf("http://localhost:%d/v1", s.port)
}

// Start launches the llama-server process.
func (s *Sidecar) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return nil
	}

	if _, err := os.Stat(s.binaryPath); err != nil {
		return fmt.Errorf("llama-server binary not found at %s: %w", s.binaryPath, err)
	}
	if _, err := os.Stat(s.modelPath); err != nil {
		return fmt.Errorf("model file not found at %s: %w", s.modelPath, err)
	}

	args := []string{
		"-m", s.modelPath,
		"--port", fmt.Sprintf("%d", s.port),
		"--host", "127.0.0.1",
	}
	if s.gpuLayers > 0 {
		args = append(args, "-ngl", fmt.Sprintf("%d", s.gpuLayers))
	}
	args = append(args, s.extraArgs...)

	s.cmd = exec.CommandContext(ctx, s.binaryPath, args...)
	s.cmd.Stdout = os.Stderr // llama-server logs to stderr convention
	s.cmd.Stderr = os.Stderr

	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("starting llama-server: %w", err)
	}

	s.running = true
	log.Printf("[inference] llama-server started on port %d (model: %s)", s.port, s.modelPath)

	// Monitor process
	go func() {
		err := s.cmd.Wait()
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		if err != nil {
			log.Printf("[inference] llama-server exited: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the llama-server.
func (s *Sidecar) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running || s.cmd == nil || s.cmd.Process == nil {
		return nil
	}

	log.Printf("[inference] stopping llama-server")
	if err := s.cmd.Process.Signal(os.Interrupt); err != nil {
		return s.cmd.Process.Kill()
	}
	s.running = false
	return nil
}

// Running returns whether the sidecar process is currently running.
func (s *Sidecar) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Healthy checks if the sidecar is responding to HTTP requests.
func (s *Sidecar) Healthy(ctx context.Context) bool {
	url := fmt.Sprintf("http://localhost:%d/health", s.port)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// WaitReady polls the health endpoint until the sidecar is ready or timeout.
func (s *Sidecar) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("llama-server not ready after %v", timeout)
		case <-ticker.C:
			if s.Healthy(ctx) {
				return nil
			}
		}
	}
}
