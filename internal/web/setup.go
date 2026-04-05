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

// handleRoot serves the app. Redirects to /setup if unconfigured.
// /setup serves index.html (wizard is triggered by JS based on needs_setup).
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	// Redirect root to /setup if unconfigured
	if r.URL.Path == "/" && s.NeedsSetup() {
		http.Redirect(w, r, "/setup", http.StatusTemporaryRedirect)
		return
	}
	// /setup serves the same index.html — wizard is a JS-driven screen
	if r.URL.Path == "/setup" {
		r.URL.Path = "/"
		s.staticHandler.ServeHTTP(w, r)
		return
	}
	// Everything else: normal static files
	s.staticHandler.ServeHTTP(w, r)
}
