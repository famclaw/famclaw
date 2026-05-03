package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/famclaw/famclaw/internal/store"
)

func (s *Server) handleUnknownAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !s.verifyParentPINConstantTime(r.Header.Get("X-Parent-PIN")) {
		jsonErr(w, fmt.Errorf("invalid PIN"), http.StatusForbidden)
		return
	}
	list, err := s.identStore.ListUnknown(r.Context())
	if err != nil {
		jsonErr(w, err, http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []store.UnknownAccount{}
	}
	jsonOK(w, list)
}

func (s *Server) handleUnknownAccountLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !s.verifyParentPINConstantTime(r.Header.Get("X-Parent-PIN")) {
		jsonErr(w, fmt.Errorf("invalid PIN"), http.StatusForbidden)
		return
	}
	var body struct {
		Gateway    string `json:"gateway"`
		ExternalID string `json:"external_id"`
		UserName   string `json:"user_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, err, http.StatusBadRequest)
		return
	}
	if body.Gateway == "" || body.ExternalID == "" || body.UserName == "" {
		jsonErr(w, fmt.Errorf("gateway, external_id, user_name required"), http.StatusBadRequest)
		return
	}

	s.cfgMu.RLock()
	var found bool
	for _, u := range s.cfg.Users {
		if u.Name == body.UserName {
			found = true
			break
		}
	}
	s.cfgMu.RUnlock()
	if !found {
		jsonErr(w, fmt.Errorf("unknown user_name %q", body.UserName), http.StatusBadRequest)
		return
	}

	if err := s.identStore.LinkAndClearUnknown(r.Context(), body.UserName, body.Gateway, body.ExternalID); err != nil {
		jsonErr(w, err, http.StatusInternalServerError)
		return
	}
	go s.broadcastDashboardUpdate(context.Background())
	jsonOK(w, map[string]string{"status": "linked"})
}

func (s *Server) handleUnknownAccountDismiss(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !s.verifyParentPINConstantTime(r.Header.Get("X-Parent-PIN")) {
		jsonErr(w, fmt.Errorf("invalid PIN"), http.StatusForbidden)
		return
	}
	var body struct {
		Gateway    string `json:"gateway"`
		ExternalID string `json:"external_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, err, http.StatusBadRequest)
		return
	}
	if body.Gateway == "" || body.ExternalID == "" {
		jsonErr(w, fmt.Errorf("gateway, external_id required"), http.StatusBadRequest)
		return
	}
	if err := s.identStore.ClearUnknown(r.Context(), body.Gateway, body.ExternalID); err != nil {
		jsonErr(w, err, http.StatusInternalServerError)
		return
	}
	go s.broadcastDashboardUpdate(context.Background())
	jsonOK(w, map[string]string{"status": "dismissed"})
}
