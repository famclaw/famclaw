package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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

// webFetchSettingsView carries the web_fetch allowlist and enable flag.
type webFetchSettingsView struct {
	Enabled      bool     `json:"enabled"`
	URLAllowlist []string `json:"url_allowlist"`
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
	// Copy the allowlist so the view is stable against concurrent writes
	view.WebFetch.URLAllowlist = make([]string, len(s.cfg.Tools.WebFetch.URLAllowlist))
	copy(view.WebFetch.URLAllowlist, s.cfg.Tools.WebFetch.URLAllowlist)

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

	// LLM config — legacy single endpoint
	if update.LLM.BaseURL != "" {
		s.cfg.LLM.BaseURL = update.LLM.BaseURL
	}
	if update.LLM.Model != "" {
		s.cfg.LLM.Model = update.LLM.Model
	}
	// Only update API key if client sends a non-masked value
	if update.LLM.APIKey != "" && update.LLM.APIKey != "••••••••" {
		s.cfg.LLM.APIKey = update.LLM.APIKey
	}

	// LLM profiles — pointer fields distinguish "omitted" from "explicit clear"
	if update.LLM.Default != nil {
		s.cfg.LLM.Default = *update.LLM.Default
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
		s.cfg.LLM.Profiles = newProfiles
	}

	// Users — validate at least one parent with PIN remains
	if len(update.Users) > 0 {
		hasParentWithPIN := false
		var users []config.UserConfig
		for _, u := range update.Users {
			if u.Role == "parent" && u.PIN != "" {
				hasParentWithPIN = true
			}
			users = append(users, config.UserConfig{
				Name:        u.Name,
				DisplayName: u.DisplayName,
				Role:        u.Role,
				AgeGroup:    u.AgeGroup,
				PIN:         u.PIN,
				Color:       u.Color,
				LLMProfile:  u.LLMProfile,
			})
		}
		if !hasParentWithPIN {
			jsonErr(w, fmt.Errorf("at least one parent user with a PIN is required"), http.StatusBadRequest)
			return
		}
		s.cfg.Users = users
	}

	// Gateways
	s.cfg.Gateways.Telegram.Enabled = update.Gateways.Telegram.Enabled
	if update.Gateways.Telegram.Token != "" {
		s.cfg.Gateways.Telegram.Token = update.Gateways.Telegram.Token
	}
	s.cfg.Gateways.Discord.Enabled = update.Gateways.Discord.Enabled
	if update.Gateways.Discord.Token != "" {
		s.cfg.Gateways.Discord.Token = update.Gateways.Discord.Token
	}

	// WebFetch — allowlist and enable flag
	// Empty update list is ignored (client didn't send it); non-empty replaces
	// the allowlist entirely. Enabled flag can be toggled independently.
	if update.WebFetch.Enabled != s.cfg.Tools.WebFetch.Enabled {
		s.cfg.Tools.WebFetch.Enabled = update.WebFetch.Enabled
	}
	if len(update.WebFetch.URLAllowlist) > 0 {
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
		s.cfg.Tools.WebFetch.URLAllowlist = deduped
	}

	// Validate web_fetch config after mutation
	if s.cfg.Tools.WebFetch.Enabled && len(s.cfg.Tools.WebFetch.URLAllowlist) == 0 {
		jsonErr(w, fmt.Errorf("web_fetch is enabled but url_allowlist is empty — set at least one host or disable web_fetch"), http.StatusBadRequest)
		return
	}

	// Write back to config.yaml
	if s.cfgPath != "" {
		if err := s.writeConfig(); err != nil {
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
	// Prepend warning — yaml.Marshal strips comments from original file
	header := "# FamClaw configuration (managed by web UI)\n# Edit via the Settings page in the web UI, or edit this file and restart.\n\n"
	if err := os.WriteFile(s.cfgPath, append([]byte(header), data...), 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

// NeedsSetup returns true if the LLM is not fully configured.
func (s *Server) NeedsSetup() bool {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.LLM.BaseURL == "" || s.cfg.LLM.Model == ""
}
