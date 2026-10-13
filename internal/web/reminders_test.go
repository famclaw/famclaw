package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/store"
)

// TestUndeliveredRemindersHandler exercises the /api/reminders/undelivered
// handler: a delivery failure must be queryable by the user (newest first),
// and the payload shape is locked in. Auth is enforced by the session
// middleware on the protected route and covered elsewhere (see
// TestUnknownAccounts_GET).
func TestUndeliveredRemindersHandler(t *testing.T) {
	cases := []struct {
		name       string
		seed       bool
		method     string
		wantStatus int
		wantLen    int
	}{
		{"empty", false, http.MethodGet, http.StatusOK, 0},
		{"with rows", true, http.MethodGet, http.StatusOK, 2},
		{"method not allowed", false, http.MethodPost, http.StatusMethodNotAllowed, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, cleanup := newTestServer(t)
			defer cleanup()

			if tc.seed {
				ctx := context.Background()
				for _, rec := range []store.UndeliveredReminder{
					{ReminderID: 1, UserName: "julia", Gateway: "discord", ChatID: "ch-1", Message: "first", Reason: "invalid_chat"},
					{ReminderID: 2, UserName: "julia", Gateway: "discord", ChatID: "ch-1", Message: "second", Reason: "retries_exhausted"},
				} {
					if err := srv.db.SaveUndeliveredReminder(ctx, &rec); err != nil {
						t.Fatalf("SaveUndeliveredReminder: %v", err)
					}
				}
			}

			req := httptest.NewRequest(tc.method, "/api/reminders/undelivered", nil)
			rec := httptest.NewRecorder()
			srv.handleUndeliveredReminders(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			var items []store.UndeliveredReminder
			if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
				t.Fatalf("response is not a JSON array: %v (body: %s)", err, rec.Body.String())
			}
			if len(items) != tc.wantLen {
				t.Fatalf("items = %d, want %d", len(items), tc.wantLen)
			}
			if tc.wantLen > 0 {
				// Newest first: the second (higher id) row comes back first.
				if items[0].ReminderID != 2 {
					t.Errorf("newest first: items[0].ReminderID = %d, want 2", items[0].ReminderID)
				}
				for _, want := range []string{"first", "second"} {
					found := false
					for _, it := range items {
						if it.Message == want {
							found = true
						}
					}
					if !found {
						t.Errorf("content %q missing from response — it must never be silently lost", want)
					}
				}
				if !strings.Contains(items[0].Reason, "_") {
					t.Errorf("reason should be a machine-readable identifier, got %q", items[0].Reason)
				}
			}
		})
	}
}
