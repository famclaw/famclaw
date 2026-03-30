package web

import (
	"net/http"

	"github.com/famclaw/famclaw/internal/hardware"
)

// handleSetupDetect returns hardware capabilities for the first-boot wizard.
// Only available during first boot (no users configured).
func (s *Server) handleSetupDetect(w http.ResponseWriter, r *http.Request) {
	if !s.isFirstBoot() {
		http.Error(w, "setup already complete", http.StatusForbidden)
		return
	}
	info := hardware.Detect()
	jsonOK(w, info)
}
