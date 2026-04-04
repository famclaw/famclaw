package web

import (
	"net/http"

	"github.com/famclaw/famclaw/internal/hardware"
)

// handleSetupDetect returns hardware capabilities for the setup wizard.
// Only available during first boot (no parent with PIN configured).
func (s *Server) handleSetupDetect(w http.ResponseWriter, r *http.Request) {
	if !s.isFirstBoot() {
		http.Error(w, "setup already complete", http.StatusForbidden)
		return
	}
	info := hardware.Detect()
	jsonOK(w, info)
}

// handleSetupRedirect redirects to /setup if the system needs configuration,
// otherwise serves the normal app.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" && s.NeedsSetup() {
		http.Redirect(w, r, "/setup", http.StatusTemporaryRedirect)
		return
	}
	// Serve static files for all other paths
	s.staticHandler.ServeHTTP(w, r)
}
