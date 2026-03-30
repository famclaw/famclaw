package web

import (
	"net/http"

	"github.com/famclaw/famclaw/internal/hardware"
)

// handleSetupDetect returns hardware capabilities for the first-boot wizard.
func (s *Server) handleSetupDetect(w http.ResponseWriter, r *http.Request) {
	info := hardware.Detect()
	jsonOK(w, info)
}
