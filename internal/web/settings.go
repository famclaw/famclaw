package web

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/famclaw/famclaw/internal/config"
	"gopkg.in/yaml.v3"
)

// Note: s.clientsMu on Server is used for WebSocket clients.
// s.cfgMu (below, added to Server struct) guards config reads/writes.

// settingsView is the JSON shape for GET/POST /api/settings.
type settingsView struct {
	LLM      llmSettingsView     `json:"llm"`
	Users    []userSettingsView  `json:"users"`
	Gateways gatewaySettingsView `json:"gateways"`
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
	Default  string                     `json:"default,omitempty"`
	Profiles map[string]llmProfileView  `json:"profiles,omitempty"`
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
	WhatsApp struct {
		Enabled bool   `json:"enabled"`
		DBPath  string `json:"db_path,omitempty"`
	} `json:"whatsapp"`
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

	view := settingsView{
		LLM: llmSettingsView{
			BaseURL: s.cfg.LLM.BaseURL,
			Model:   s.cfg.LLM.Model,
			Default: s.cfg.LLM.Default,
		},
	}

	// Mask API key — never expose in GET
	if s.cfg.LLM.APIKey != "" {
		view.LLM.APIKey = "••••••••"
	}

	// Include named profiles
	if len(s.cfg.LLM.Profiles) > 0 {
		view.LLM.Profiles = make(map[string]llmProfileView, len(s.cfg.LLM.Profiles))
		for name, p := range s.cfg.LLM.Profiles {
			pv := llmProfileView{
				Label:   p.Label,
				BaseURL: p.BaseURL,
				Model:   p.Model,
			}
			if p.APIKey != "" {
				pv.APIKey = "••••••••"
			}
			view.LLM.Profiles[name] = pv
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
	view.Gateways.WhatsApp.Enabled = s.cfg.Gateways.WhatsApp.Enabled

	jsonOK(w, view)
}

func (s *Server) handleSettingsPost(w http.ResponseWriter, r *http.Request) {
	// On first boot (no users configured), skip PIN check so the wizard can
	// create the first parent user. After that, PIN is always required.
	if !s.isFirstBoot() {
		pin := r.Header.Get("X-Parent-PIN")
		if !s.verifyParentPINConstantTime(pin) {
			jsonErr(w, fmt.Errorf("invalid PIN"), http.StatusForbidden)
			return
		}
	}

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

	// LLM profiles
	if update.LLM.Default != "" {
		s.cfg.LLM.Default = update.LLM.Default
	}
	if len(update.LLM.Profiles) > 0 {
		if s.cfg.LLM.Profiles == nil {
			s.cfg.LLM.Profiles = make(map[string]config.LLMProfile)
		}
		for name, pv := range update.LLM.Profiles {
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
			s.cfg.LLM.Profiles[name] = p
		}
		// Remove profiles not in the update (full replacement)
		for name := range s.cfg.LLM.Profiles {
			if _, ok := update.LLM.Profiles[name]; !ok {
				delete(s.cfg.LLM.Profiles, name)
			}
		}
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
	s.cfg.Gateways.WhatsApp.Enabled = update.Gateways.WhatsApp.Enabled

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
	header := "# FamClaw configuration (managed by web UI)\n# Edit via http://famclaw.local:8080 settings, or edit this file and restart.\n\n"
	if err := os.WriteFile(s.cfgPath, append([]byte(header), data...), 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

// verifyParentPINConstantTime checks the PIN using constant-time comparison.
func (s *Server) verifyParentPINConstantTime(pin string) bool {
	for _, u := range s.cfg.Users {
		if u.Role == "parent" && u.PIN != "" {
			if subtle.ConstantTimeCompare([]byte(pin), []byte(u.PIN)) == 1 {
				return true
			}
		}
	}
	return false
}

// NeedsSetup returns true if the LLM is not fully configured.
func (s *Server) NeedsSetup() bool {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.LLM.BaseURL == "" || s.cfg.LLM.Model == ""
}

// isFirstBoot returns true if no parent users are configured yet.
// Used to skip PIN check during initial setup wizard.
func (s *Server) isFirstBoot() bool {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	for _, u := range s.cfg.Users {
		if u.Role == "parent" && u.PIN != "" {
			return false
		}
	}
	return true
}
