// shared_config.go implements shared configuration save/reload helpers
// for web_fetch denylist and allowlist with proper transactional semantics.
//
// #423 round 18 (plan §B/§C, scoped): this file is the file-commit gate
// (T = SharedConfigManager.mu). The in-memory policy slices are guarded by
// the config's ONE shared WebFetchPolicyOwner (M, see
// internal/config/policy_owner.go). Lock order is M -> T only: admin
// handlers take the owner with no other lock held, and SaveConfig takes T
// inside that span. No path takes T before M, so no cycle is possible.
//
// TODO(#423 / plan §B, reported on card t_2b14f901): live change-event
// watching (fsnotify-based reload dispatch, self-event reconciliation,
// dirty-flag propagation) is NOT implemented. The ConfigWatcher below is
// the in-process fan-out point SaveConfig uses after a confirmed commit;
// no consumer registers a listener today, and the fake ReloadDispatcher
// stub was removed in this round as dead code.
package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"

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
//
// Locking: takes T (scm.mu) around the whole read-verify-commit span
// (plan §B: the precondition read and the file commit are one serialized
// transaction). Callers must hold the shared policy owner (M) when the
// save publishes an in-memory policy mutation, so the observed order is
// always M -> T (review S2: no path takes T or M while holding a component
// lock; M and T are the two config locks, components are everything else).
func (scm *SharedConfigManager) SaveConfig(cfg interface{}, contentToken string) (string, error) {
	scm.mu.Lock()
	defer scm.mu.Unlock()

	// Validate the content token (ensure no external edits occurred)
	if err := scm.validateContentToken(contentToken); err != nil {
		return "", fmt.Errorf("%w: %v", ErrOperatorConflict, err)
	}

	// Marshal the config using YAML (following the pattern in web_fetch_allowlist.go)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshaling config: %w", err)
	}

	// Write to temporary file first
	tempPath := scm.configPath + ".tmp"
	header := "# FamClaw configuration (managed by web UI)\n# Edit via the Settings page in the web UI, or edit this file and restart.\n\n"
	// The on-disk bytes are header + YAML: every token in this manager is
	// computed over the on-disk bytes (CurrentToken / validateContentToken
	// hash what ReadFile returns), so the new token must hash this exact
	// content, not the bare YAML.
	content := append([]byte(header), data...)
	if err := os.WriteFile(tempPath, content, 0600); err != nil {
		return "", fmt.Errorf("writing temp config file: %w", err)
	}

	// Atomic rename to replace the original file
	if err := os.Rename(tempPath, scm.configPath); err != nil {
		return "", fmt.Errorf("atomic rename: %w", err)
	}

	// Generate new content token for tracking
	newToken, err := scm.generateContentToken(content)
	if err != nil {
		return "", fmt.Errorf("generating content token: %w", err)
	}

	// Notify watchers of the change
	// Synchronous (non-blocking) notify: channel-send-with-default is O(1); no goroutine leak on shutdown. (bot finding #2, ccb942d)
	scm.watcher.NotifyChange(newToken)

	return newToken, nil
}

// ErrOperatorConflict is the sentinel returned by SaveConfig when the
// on-disk content token changed between the handler's precondition read
// (CurrentToken) and the commit: another process wrote the config file in
// between. Handlers detect it with errors.Is, do NOT publish their
// in-memory mutation (existing C is preserved, plan §B commit order), and
// surface the sentinel so the caller can re-read and retry.
var ErrOperatorConflict = errors.New("operator conflict: config file changed by another process since the precondition read")

// ErrTokenReadFailed is the sentinel returned by CurrentToken when the
// config file exists but cannot be read. A read failure means the on-disk
// state is unknown, so the save precondition cannot be established:
// handlers fail closed instead of saving against a blind overwrite
// (review S4). A missing file is NOT a read failure: it returns ("", nil),
// making the precondition vacuous by design for new files.
var ErrTokenReadFailed = errors.New("cannot read config file for content-token precondition")

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
	// SHA256 of the file content: deterministic, so two managers over the
	// same path compute the same token and the precondition read stays
	// consistent across Agents (round 15: replaced the time-based token
	// that could never detect conflicts).
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// CurrentToken returns the current content token for the config file
func (scm *SharedConfigManager) CurrentToken() (string, error) {
	scm.mu.Lock()
	defer scm.mu.Unlock()

	// If we don't have a config path, return empty string
	if scm.configPath == "" {
		return "", nil
	}

	// Read the file and generate its token
	data, err := os.ReadFile(scm.configPath)
	if err != nil {
		// A missing file is NOT a failure: for new files the save
		// precondition is vacuous by design (review S4) — the token is
		// "" and validateContentToken accepts it.
		if os.IsNotExist(err) {
			return "", nil
		}
		// Any other read failure means the on-disk state is unknown:
		// fail closed with the sentinel instead of letting callers save
		// against a blind overwrite.
		return "", fmt.Errorf("%w: %v", ErrTokenReadFailed, err)
	}

	token, err := scm.generateContentToken(data)
	if err != nil {
		return "", fmt.Errorf("cannot generate current token: %w", err)
	}
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
	// The write lock: NotifyChange assigns cw.eventToken, and a read lock
	// here would let two concurrent NotifyChange calls write eventToken
	// simultaneously (review M1).
	cw.mu.Lock()
	defer cw.mu.Unlock()

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
