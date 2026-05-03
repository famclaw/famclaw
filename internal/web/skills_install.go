package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func (s *Server) handleSkillInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !s.verifyParentPINConstantTime(r.Header.Get("X-Parent-PIN")) {
		jsonErr(w, fmt.Errorf("invalid PIN"), http.StatusForbidden)
		return
	}
	if s.skillRegistry == nil {
		jsonErr(w, fmt.Errorf("skill registry not configured"), http.StatusServiceUnavailable)
		return
	}
	var body struct {
		NameOrPath string `json:"name_or_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, fmt.Errorf("decoding body: %w", err), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.NameOrPath) == "" {
		jsonErr(w, fmt.Errorf("name_or_path is required"), http.StatusBadRequest)
		return
	}
	skill, err := s.skillRegistry.Install(r.Context(), body.NameOrPath)
	if err != nil {
		jsonErr(w, err, http.StatusInternalServerError)
		return
	}
	go s.broadcastDashboardUpdate(context.Background())
	jsonOK(w, skill)
}

func (s *Server) handleSkillRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !s.verifyParentPINConstantTime(r.Header.Get("X-Parent-PIN")) {
		jsonErr(w, fmt.Errorf("invalid PIN"), http.StatusForbidden)
		return
	}
	if s.skillRegistry == nil {
		jsonErr(w, fmt.Errorf("skill registry not configured"), http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, fmt.Errorf("decoding body: %w", err), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		jsonErr(w, fmt.Errorf("name is required"), http.StatusBadRequest)
		return
	}
	if err := s.skillRegistry.Remove(body.Name); err != nil {
		jsonErr(w, err, http.StatusInternalServerError)
		return
	}
	go s.broadcastDashboardUpdate(context.Background())
	jsonOK(w, map[string]string{"status": "removed", "name": body.Name})
}
