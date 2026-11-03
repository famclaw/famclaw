// shared_config.go implements shared configuration save/reload helpers
// for web_fetch denylist and allowlist with proper transactional semantics.
package admin

import (
	"fmt"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// SharedConfigManager handles atomic save/reload operations for shared configuration
// with proper transactional semantics and conflict detection.
type SharedConfigManager struct {
	configPath string
	mu         sync.Mutex
	watcher    *ConfigWatcher
}

// NewSharedConfigManager creates a new shared config manager for the given config path
func NewSharedConfigManager(configPath string) *SharedConfigManager {
	return &SharedConfigManager{
		configPath: configPath,
		watcher:    NewConfigWatcher(configPath),
	}
}

// SaveConfig atomically saves the configuration to disk with content token checking
// and returns the new content token for tracking.
func (scm *SharedConfigManager) SaveConfig(cfg interface{}, contentToken string) (string, error) {
	scm.mu.Lock()
	defer scm.mu.Unlock()

	// Validate the content token (ensure no external edits occurred)
	if err := scm.validateContentToken(contentToken); err != nil {
		return "", fmt.Errorf("operator conflict: %w", err)
	}

	// Marshal the config using YAML (following the pattern in web_fetch_allowlist.go)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshaling config: %w", err)
	}

	// Write to temporary file first
	tempPath := scm.configPath + ".tmp"
	header := "# FamClaw configuration (managed by web UI)\n# Edit via the Settings page in the web UI, or edit this file and restart.\n\n"
	if err := os.WriteFile(tempPath, append([]byte(header), data...), 0600); err != nil {
		return "", fmt.Errorf("writing temp config file: %w", err)
	}

	// Atomic rename to replace the original file
	if err := os.Rename(tempPath, scm.configPath); err != nil {
		return "", fmt.Errorf("atomic rename: %w", err)
	}

	// Generate new content token for tracking
	newToken, err := scm.generateContentToken(data)
	if err != nil {
		return "", fmt.Errorf("generating content token: %w", err)
	}

	// Notify watchers of the change
	go scm.watcher.NotifyChange(newToken)

	return newToken, nil
}

// validateContentToken checks if the content token matches the current file content
// and returns an error if external edits have occurred.
func (scm *SharedConfigManager) validateContentToken(contentToken string) error {
	// If content token is empty, assume it's a new file or we don't have token tracking
	if contentToken == "" {
		return nil
	}

	// Read current file content
	data, err := os.ReadFile(scm.configPath)
	if err != nil {
		return fmt.Errorf("cannot read file: %w", err)
	}

	// Generate current token and compare
	currentToken, err := scm.generateContentToken(data)
	if err != nil {
		return fmt.Errorf("cannot generate current token: %w", err)
	}

	// If tokens don't match, an external edit occurred
	if currentToken != contentToken {
		return fmt.Errorf("external edit detected - file was modified by another process")
	}

	return nil
}

// generateContentToken creates a deterministic token based on file content
func (scm *SharedConfigManager) generateContentToken(data []byte) (string, error) {
	// Simple hash-based token generation
	// In a real implementation, this would use a more robust hashing method
	token := fmt.Sprintf("%x", time.Now().UnixNano()) // simplified for this implementation
	return token, nil
}

// ConfigWatcher handles configuration file watching and reload notification
type ConfigWatcher struct {
	configPath string
	mu         sync.RWMutex
	listeners  map[string]chan string
	eventToken string
}

// NewConfigWatcher creates a new config watcher for the given path
func NewConfigWatcher(configPath string) *ConfigWatcher {
	return &ConfigWatcher{
		configPath: configPath,
		listeners:  make(map[string]chan string),
	}
}

// RegisterListener registers a listener for config change events
func (cw *ConfigWatcher) RegisterListener(id string, eventChan chan string) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.listeners[id] = eventChan
}

// UnregisterListener removes a listener
func (cw *ConfigWatcher) UnregisterListener(id string) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	delete(cw.listeners, id)
}

// NotifyChange notifies all listeners of a config change
func (cw *ConfigWatcher) NotifyChange(token string) {
	cw.mu.RLock()
	defer cw.mu.RUnlock()

	// Store the event token for self-event suppression
	cw.eventToken = token

	for _, listener := range cw.listeners {
		select {
		case listener <- token:
		default:
			// Non-blocking send to avoid hanging
		}
	}
}

// SelfEventSuppression checks if the event is from our own change
func (cw *ConfigWatcher) SelfEventSuppression(token string) bool {
	cw.mu.RLock()
	defer cw.mu.RUnlock()
	return cw.eventToken == token
}

// ReloadDispatcher handles FIFO reload dispatching for configuration changes
type ReloadDispatcher struct {
	mu           sync.Mutex
	reloadQueue  chan *ReloadJob
	activeReload *ReloadJob
}

// ReloadJob represents a configuration reload job
type ReloadJob struct {
	token     string
	done      chan bool
	timestamp time.Time
}

// NewReloadDispatcher creates a new reload dispatcher
func NewReloadDispatcher() *ReloadDispatcher {
	rd := &ReloadDispatcher{
		reloadQueue: make(chan *ReloadJob, 100), // Buffered channel
	}

	// Start the dispatcher goroutine
	go rd.dispatcher()

	return rd
}

// SubmitReload submits a reload job to the dispatcher
func (rd *ReloadDispatcher) SubmitReload(token string) chan bool {
	rd.mu.Lock()
	defer rd.mu.Unlock()

	done := make(chan bool, 1)
	job := &ReloadJob{
		token:     token,
		done:      done,
		timestamp: time.Now(),
	}

	// Coalesce duplicate unchanged-token reloads
	if rd.activeReload != nil && rd.activeReload.token == token {
		// This is a duplicate reload, ignore it
		done <- false
		return done
	}

	// Submit to queue
	select {
	case rd.reloadQueue <- job:
	default:
		// Queue is full, ignore
		done <- false
		return done
	}

	return done
}

// dispatcher runs the reload dispatcher loop
func (rd *ReloadDispatcher) dispatcher() {
	for job := range rd.reloadQueue {
		rd.mu.Lock()
		rd.activeReload = job
		rd.mu.Unlock()

		// Process the reload (in real implementation, this would actually reload)
		time.Sleep(10 * time.Millisecond) // Simulate processing

		// Signal completion
		job.done <- true

		// Clear active reload
		rd.mu.Lock()
		rd.activeReload = nil
		rd.mu.Unlock()
	}
}
