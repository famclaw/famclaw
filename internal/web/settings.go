package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/famclaw/famclaw/internal/config"
	"gopkg.in/yaml.v3"
)

// Note: s.clientsMu on Server is used for WebSocket clients.
// s.cfgMu (below, added to Server struct) guards config reads/writes.

// settingsView is the JSON shape for GET/POST /api/settings.
type settingsView struct {
	LLM      llmSettingsView      `json:"llm"`
	Users    []userSettingsView   `json:"users"`
	Gateways gatewaySettingsView  `json:"gateways"`
	WebFetch webFetchSettingsView `json:"web_fetch"`
}

type llmProfileView struct {
	Label   string `json:"label"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	APIKey  string `json:"api_key,omitempty"`
}

type llmSettingsView struct {
	BaseURL  string                     `json:"base_url"`
	Model    string                     `json:"model"`
	APIKey   string                     `json:"api_key,omitempty"`
	Default  *string                    `json:"default,omitempty"`
	Profiles *map[string]llmProfileView `json:"profiles,omitempty"`
}

type userSettingsView struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	AgeGroup    string `json:"age_group,omitempty"`
	PIN         string `json:"pin,omitempty"`
	Color       string `json:"color,omitempty"`
	LLMProfile  string `json:"llm_profile,omitempty"`
}

type gatewaySettingsView struct {
	Telegram struct {
		Enabled bool   `json:"enabled"`
		Token   string `json:"token,omitempty"`
	} `json:"telegram"`
	Discord struct {
		Enabled bool   `json:"enabled"`
		Token   string `json:"token,omitempty"`
	} `json:"discord"`
}

// webFetchSettingsView carries the web_fetch URL policy, allowlist, and
// denylist. The denylist + bundled baseline are the kid-safe default (W2);
// the allowlist is the back-compat parent-maintained mode.
type webFetchSettingsView struct {
	Enabled      bool     `json:"enabled"`
	URLPolicy    string   `json:"url_policy,omitempty"`
	URLAllowlist []string `json:"url_allowlist"`
	URLDenylist  []string `json:"url_denylist"`
}

// handleSettings handles GET (read config) and POST (update config).
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		s.handleSettingsGet(w, r)
	case "POST":
		s.handleSettingsPost(w, r)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()

	// The view copy below reads the URLDenylist slice header; take the
	// shared WebFetch policy owner (cfg.WebFetchPolicyOwner(); see
	// policy_owner.go for the accessor list) so it cannot tear against a
	// concurrent settings-POST replace or an admin denylist tool rewrite.
	// Lock order: cfgMu, then the owner (never reversed).
	owner := s.cfg.WebFetchPolicyOwner()
	owner.RLock()
	defer owner.RUnlock()

	defCopy := s.cfg.LLM.Default
	view := settingsView{
		LLM: llmSettingsView{
			BaseURL: s.cfg.LLM.BaseURL,
			Model:   s.cfg.LLM.Model,
			Default: &defCopy,
		},
	}

	// Mask API key — never expose in GET
	if s.cfg.LLM.APIKey != "" {
		view.LLM.APIKey = "••••••••"
	}

	// Include named profiles
	if len(s.cfg.LLM.Profiles) > 0 {
		pm := make(map[string]llmProfileView, len(s.cfg.LLM.Profiles))
		view.LLM.Profiles = &pm
		for name, p := range s.cfg.LLM.Profiles {
			pv := llmProfileView{
				Label:   p.Label,
				BaseURL: p.BaseURL,
				Model:   p.Model,
			}
			if p.APIKey != "" {
				pv.APIKey = "••••••••"
			}
			(*view.LLM.Profiles)[name] = pv
		}
	}

	for _, u := range s.cfg.Users {
		view.Users = append(view.Users, userSettingsView{
			Name:        u.Name,
			DisplayName: u.DisplayName,
			Role:        u.Role,
			AgeGroup:    u.AgeGroup,
			Color:       u.Color,
			LLMProfile:  u.LLMProfile,
		})
	}

	view.Gateways.Telegram.Enabled = s.cfg.Gateways.Telegram.Enabled
	view.Gateways.Discord.Enabled = s.cfg.Gateways.Discord.Enabled

	// WebFetch settings
	view.WebFetch.Enabled = s.cfg.Tools.WebFetch.Enabled
	view.WebFetch.URLPolicy = s.cfg.Tools.WebFetch.URLPolicy
	// Copy the allowlist so the view is stable against concurrent writes
	view.WebFetch.URLAllowlist = make([]string, len(s.cfg.Tools.WebFetch.URLAllowlist))
	copy(view.WebFetch.URLAllowlist, s.cfg.Tools.WebFetch.URLAllowlist)
	view.WebFetch.URLDenylist = make([]string, len(s.cfg.Tools.WebFetch.URLDenylist))
	copy(view.WebFetch.URLDenylist, s.cfg.Tools.WebFetch.URLDenylist)

	jsonOK(w, view)
}

func (s *Server) handleSettingsPost(w http.ResponseWriter, r *http.Request) {
	// Auth is enforced by the session middleware on the protected route. The
	// first-boot wizard hits this through the same gate, but Phase 7's
	// /api/setup/pin endpoint runs the PIN bootstrap before this is invoked,
	// so by the time we get here a session cookie is always present.

	var update settingsView
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		jsonErr(w, err, http.StatusBadRequest)
		return
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	// This handler rewrites cfg.Tools.WebFetch.URLDenylist (the section
	// replace below and the rollback), reads it (the newWebFetch and
	// snapWebFetch copies), and marshals the whole config for persistence —
	// every one of those must serialize with the agent's hostDecision
	// reads and the admin denylist/allowlist tools, which all take the
	// shared WebFetch policy owner on the *Config both sides hold
	// (cfg.WebFetchPolicyOwner(); see policy_owner.go for the full
	// accessor list). cfgMu alone did not cover them — that two-lock gap
	// was PR #423's current-head data-race finding. The owner lives on the
	// config object because the web server has no reference to the agent
	// (agents are per-turn) and the agent has no reference to this
	// server's cfgMu. Held for the whole POST: lock order cfgMu → owner,
	// acyclic (no path takes cfgMu while holding the owner).
	//
	// Disk I/O under the owner (the writeConfig() below) is a re-resolved
	// ACCEPTED DEFERRAL, not a bug: the in-memory publish and the rollback
	// on a failed write must stay serialized with readers, or a
	// hostDecision reader could observe a published-then-reverted or
	// half-applied config. The stall is bounded and infrequent — a
	// settings POST is a rare parent action. The future path, if it ever
	// matters, is to publish in-memory under the owner (M) and write to
	// disk under the commit lock (T) only — keeping the temp+rename
	// atomicity, not dropping it. If that split is ever made, update this
	// comment and the policy_owner.go lock-order doc to reflect the new
	// span boundary.
	owner := s.cfg.WebFetchPolicyOwner()
	owner.Lock()
	defer owner.Unlock()

	// Build updated config values first, validate everything, then assign
	// LLM config — legacy single endpoint
	newLLM := s.cfg.LLM
	if update.LLM.BaseURL != "" {
		newLLM.BaseURL = update.LLM.BaseURL
	}
	if update.LLM.Model != "" {
		newLLM.Model = update.LLM.Model
	}
	// Only update API key if client sends a non-masked value
	if update.LLM.APIKey != "" && update.LLM.APIKey != "••••••••" {
		newLLM.APIKey = update.LLM.APIKey
	}

	// LLM profiles — pointer fields distinguish "omitted" from "explicit clear"
	if update.LLM.Default != nil {
		newLLM.Default = *update.LLM.Default
	}
	if update.LLM.Profiles != nil {
		profiles := *update.LLM.Profiles
		newProfiles := make(map[string]config.LLMProfile, len(profiles))
		for name, pv := range profiles {
			p := config.LLMProfile{
				Label:   pv.Label,
				BaseURL: pv.BaseURL,
				Model:   pv.Model,
			}
			// Only update API key if non-masked
			if pv.APIKey != "" && pv.APIKey != "••••••••" {
				p.APIKey = pv.APIKey
			} else if existing, ok := s.cfg.LLM.Profiles[name]; ok {
				p.APIKey = existing.APIKey
			}
			newProfiles[name] = p
		}
		newLLM.Profiles = newProfiles
	}

	// Users — validate at least one parent with PIN remains
	var newUsers []config.UserConfig
	if len(update.Users) > 0 {
		hasParentWithPIN := false
		for _, u := range update.Users {
			// Preserve existing PIN if not provided in update
			pin := u.PIN
			if pin == "" {
				// Find existing user (case-insensitive, matching GetUser) to preserve PIN
				if existingUser := s.cfg.GetUser(u.Name); existingUser != nil {
					pin = existingUser.PIN
				}
			}

			if u.Role == "parent" && pin != "" {
				hasParentWithPIN = true
			}
			newUsers = append(newUsers, config.UserConfig{
				Name:        u.Name,
				DisplayName: u.DisplayName,
				Role:        u.Role,
				AgeGroup:    u.AgeGroup,
				PIN:         pin,
				Color:       u.Color,
				LLMProfile:  u.LLMProfile,
			})
		}
		if !hasParentWithPIN {
			jsonErr(w, fmt.Errorf("at least one parent user with a PIN is required"), http.StatusBadRequest)
			return
		}
	} else {
		// If no users provided, keep existing users
		newUsers = s.cfg.Users
	}

	// Gateways
	newGateways := s.cfg.Gateways
	newGateways.Telegram.Enabled = update.Gateways.Telegram.Enabled
	if update.Gateways.Telegram.Token != "" {
		newGateways.Telegram.Token = update.Gateways.Telegram.Token
	}
	newGateways.Discord.Enabled = update.Gateways.Discord.Enabled
	if update.Gateways.Discord.Token != "" {
		newGateways.Discord.Token = update.Gateways.Discord.Token
	}

	// WebFetch — policy, allowlist, denylist, and enable flag
	newWebFetch := s.cfg.Tools.WebFetch
	if update.WebFetch.Enabled != s.cfg.Tools.WebFetch.Enabled {
		newWebFetch.Enabled = update.WebFetch.Enabled
	}
	if update.WebFetch.URLPolicy != "" {
		// Only accept an explicit policy override when it's a known mode.
		switch strings.ToLower(strings.TrimSpace(update.WebFetch.URLPolicy)) {
		case "allowlist", "denylist":
			newWebFetch.URLPolicy = strings.ToLower(strings.TrimSpace(update.WebFetch.URLPolicy))
		default:
			jsonErr(w, fmt.Errorf("web_fetch.url_policy must be %q or %q", "allowlist", "denylist"), http.StatusBadRequest)
			return
		}
	}
	// Preserve an omitted list and apply an explicit empty array.
	if update.WebFetch.URLAllowlist != nil {
		// Deduplicate, trim whitespace, and validate hosts
		seen := make(map[string]struct{}, len(update.WebFetch.URLAllowlist))
		deduped := make([]string, 0, len(update.WebFetch.URLAllowlist))
		for _, host := range update.WebFetch.URLAllowlist {
			trimmed := strings.TrimSpace(host)
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; !ok {
				if _, err := config.NormalizeAllowlistHost(trimmed); err != nil {
					jsonErr(w, fmt.Errorf("invalid host %q: %w", trimmed, err), http.StatusBadRequest)
					return
				}
				seen[trimmed] = struct{}{}
				deduped = append(deduped, trimmed)
			}
		}
		newWebFetch.URLAllowlist = deduped
	}
	// JSON unmarshal semantics carry the "present vs absent" distinction:
	// an absent url_denylist decodes to a nil slice (keep the existing
	// list), while an explicit "url_denylist": [] decodes to a non-nil
	// empty slice (clear the list). The old "len > 0" guard collapsed the
	// two, so a parent could add denylist hosts but never clear the
	// denylist via /api/settings (PR #423 finding). Guard on nil instead
	// so the explicit empty array applies and clears.
	if update.WebFetch.URLDenylist != nil {
		// Deduplicate, trim whitespace, and validate hosts (same shape as allowlist).
		seen := make(map[string]struct{}, len(update.WebFetch.URLDenylist))
		deduped := make([]string, 0, len(update.WebFetch.URLDenylist))
		for _, host := range update.WebFetch.URLDenylist {
			trimmed := strings.TrimSpace(host)
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; !ok {
				if _, err := config.NormalizeAllowlistHost(trimmed); err != nil {
					jsonErr(w, fmt.Errorf("invalid host %q: %w", trimmed, err), http.StatusBadRequest)
					return
				}
				seen[trimmed] = struct{}{}
				deduped = append(deduped, trimmed)
			}
		}
		newWebFetch.URLDenylist = deduped
	}

	// Validate web_fetch config before assignment. In allowlist mode an empty
	// allowlist would deny all fetches — reject that. In denylist mode (the
	// zero-config default) an empty allowlist is expected and valid; the
	// bundled baseline + any denylist entries still gate access.
	if newWebFetch.Enabled && newWebFetch.ResolvedURLPolicy() == "allowlist" && len(newWebFetch.URLAllowlist) == 0 {
		jsonErr(w, fmt.Errorf("web_fetch is enabled in allowlist mode but url_allowlist is empty — add at least one host, or set url_policy: denylist"), http.StatusBadRequest)
		return
	}

	// Snapshot the sections about to be replaced so a failed write can roll
	// memory back to the on-disk state.
	snapLLM := s.cfg.LLM
	if s.cfg.LLM.Profiles != nil {
		snapLLM.Profiles = make(map[string]config.LLMProfile, len(s.cfg.LLM.Profiles))
		for name, p := range s.cfg.LLM.Profiles {
			snapLLM.Profiles[name] = p
		}
	}
	snapUsers := make([]config.UserConfig, len(s.cfg.Users))
	copy(snapUsers, s.cfg.Users)
	snapGateways := s.cfg.Gateways
	// Snapshot only the section this handler owns. A whole-struct
	// snapshot + rollback write (the old `s.cfg.Tools = snapTools`)
	// would re-store every Tools sub-field — including WebSearch,
	// which handleWebSearch reads lock-free on every chat turn
	// (agent.go) — so the rollback raced a live reader. Snapshotting
	// and restoring just WebFetch keeps the write scoped to the one
	// section this handler mutates (#423 followup 2).
	snapWebFetch := s.cfg.Tools.WebFetch
	if s.cfg.Tools.WebFetch.URLAllowlist != nil {
		snapWebFetch.URLAllowlist = make([]string, len(s.cfg.Tools.WebFetch.URLAllowlist))
		copy(snapWebFetch.URLAllowlist, s.cfg.Tools.WebFetch.URLAllowlist)
	}

	// Assign all validated values at once
	s.cfg.LLM = newLLM
	s.cfg.Users = newUsers
	s.cfg.Gateways = newGateways
	s.cfg.Tools.WebFetch = newWebFetch

	// Write back to config.yaml
	if s.cfgPath != "" {
		if err := s.writeConfig(); err != nil {
			s.cfg.LLM = snapLLM
			s.cfg.Users = snapUsers
			s.cfg.Gateways = snapGateways
			s.cfg.Tools.WebFetch = snapWebFetch
			jsonErr(w, fmt.Errorf("saving config: %w", err), http.StatusInternalServerError)
			return
		}
	}

	jsonOK(w, map[string]string{"status": "saved"})
}

func (s *Server) writeConfig() error {
	data, err := yaml.Marshal(s.cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	header := "# FamClaw configuration (managed by web UI)\n# Edit via the Settings page in the web UI, or edit this file and restart.\n\n"
	content := append([]byte(header), data...)

	// Atomic write: create temp file in same directory, write, close, rename.
	// Mirrors config.Config.Save. s.cfg is NOT mutated on any failure path.
	dir := filepath.Dir(s.cfgPath)
	tmp, err := os.CreateTemp(dir, ".settings-")
	if err != nil {
		return fmt.Errorf("creating temp config: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing temp config: %w", err)
	}
	if err := os.Rename(tmpName, s.cfgPath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("renaming temp config: %w", err)
	}
	return nil
}

// NeedsSetup returns true if the LLM is not fully configured.
func (s *Server) NeedsSetup() bool {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.LLM.BaseURL == "" || s.cfg.LLM.Model == ""
}
