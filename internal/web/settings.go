package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/famclaw/famclaw/internal/config"
	"gopkg.in/yaml.v3"
)

// settingsView is the JSON shape for GET/POST /api/settings.
type settingsView struct {
	LLM      llmSettingsView      `json:"llm"`
	Users    []userSettingsView   `json:"users"`
	Gateways gatewaySettingsView  `json:"gateways"`
}

type llmSettingsView struct {
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key,omitempty"`
}

type userSettingsView struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	AgeGroup    string `json:"age_group,omitempty"`
	PIN         string `json:"pin,omitempty"`
	Color       string `json:"color,omitempty"`
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
// Requires parent PIN for writes.
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
	view := settingsView{
		LLM: llmSettingsView{
			BaseURL: s.cfg.LLM.BaseURL,
			Model:   s.cfg.LLM.Model,
			// Don't expose API key in GET — return masked
		},
		Gateways: gatewaySettingsView{},
	}

	// Mask API key
	if s.cfg.LLM.BaseURL != "" && s.cfg.LLM.BaseURL != "http://localhost:11434" {
		view.LLM.APIKey = "••••••••" // masked
	}

	for _, u := range s.cfg.Users {
		view.Users = append(view.Users, userSettingsView{
			Name:        u.Name,
			DisplayName: u.DisplayName,
			Role:        u.Role,
			AgeGroup:    u.AgeGroup,
			Color:       u.Color,
		})
	}

	view.Gateways.Telegram.Enabled = s.cfg.Gateways.Telegram.Enabled
	view.Gateways.Discord.Enabled = s.cfg.Gateways.Discord.Enabled
	view.Gateways.WhatsApp.Enabled = s.cfg.Gateways.WhatsApp.Enabled

	jsonOK(w, view)
}

func (s *Server) handleSettingsPost(w http.ResponseWriter, r *http.Request) {
	// Require parent PIN
	pin := r.Header.Get("X-Parent-PIN")
	if !s.verifyParentPIN(pin) {
		jsonErr(w, fmt.Errorf("invalid PIN"), http.StatusForbidden)
		return
	}

	var update settingsView
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		jsonErr(w, err, http.StatusBadRequest)
		return
	}

	// Update in-memory config
	if update.LLM.BaseURL != "" {
		s.cfg.LLM.BaseURL = update.LLM.BaseURL
	}
	if update.LLM.Model != "" {
		s.cfg.LLM.Model = update.LLM.Model
	}

	if len(update.Users) > 0 {
		var users []config.UserConfig
		for _, u := range update.Users {
			users = append(users, config.UserConfig{
				Name:        u.Name,
				DisplayName: u.DisplayName,
				Role:        u.Role,
				AgeGroup:    u.AgeGroup,
				PIN:         u.PIN,
				Color:       u.Color,
			})
		}
		s.cfg.Users = users
	}

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
	if err := os.WriteFile(s.cfgPath, data, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

// NeedsSetup returns true if the LLM endpoint is not configured.
func (s *Server) NeedsSetup() bool {
	return s.cfg.LLM.BaseURL == ""
}
