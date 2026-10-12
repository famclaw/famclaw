package web

import (
	"net/http"

	"github.com/famclaw/famclaw/internal/store"
)

// handleUndeliveredReminders lists reminders that could not be delivered
// (dead channel, exhausted retries) — newest first — so a delivery failure
// is never silently lost and the user can see what they missed. Auth is
// enforced by the session middleware on the protected route.
func (s *Server) handleUndeliveredReminders(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	items, err := s.db.GetUndeliveredReminders(r.Context(), 100)
	if err != nil {
		jsonErr(w, err, http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []store.UndeliveredReminder{}
	}
	jsonOK(w, items)
}
