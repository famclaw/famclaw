package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/famclaw/famclaw/internal/familystate"
	"github.com/famclaw/famclaw/internal/web/middleware"
)

// familyCategoryNameRE matches the same shape the admin tool enforces:
// lower-case [a-z0-9_]+, kept in one place so the web + agent surfaces
// can't drift.
var familyCategoryNameRE = regexp.MustCompile(`^[a-z0-9_]+$`)

// actorName resolves an audit-log "actor" string from the authenticated
// session. Identity carries only UserID (int64). Map back to the
// corresponding config.Users[].Name; falls back to a generic "web_user"
// label if the mapping cannot be made (the audit row still records the
// numeric UserID via other fields).
func (s *Server) actorName(ctx context.Context) string {
	id, ok := middleware.IdentityFrom(ctx)
	if !ok {
		return "web_unauthenticated"
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	idx := int(id.UserID - 1)
	if idx >= 0 && idx < len(s.cfg.Users) {
		return s.cfg.Users[idx].Name
	}
	return "web_user"
}

// requireFamilyState short-circuits with 503 when the store is not
// configured. Returns true if the request can proceed.
func (s *Server) requireFamilyState(w http.ResponseWriter) bool {
	if s.familyState == nil {
		http.Error(w, "family state not configured", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// validSubject mirrors agent.knownSubjects: config.Users names ∪ {"family"}.
func (s *Server) validSubject(subject string) bool {
	if subject == "family" {
		return true
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if s.cfg == nil {
		return false
	}
	for _, u := range s.cfg.Users {
		if u.Name == subject {
			return true
		}
	}
	return false
}

// ── /api/family-state/facts ───────────────────────────────────────────────────

type familyStateFactRequest struct {
	Category string `json:"category"`
	Subject  string `json:"subject"`
	Label    string `json:"label"`
	Value    string `json:"value"`
}

// handleFamilyStateFacts dispatches GET (list) and POST (upsert) on
// /api/family-state/facts. Session auth is enforced by s.protect; this
// handler trusts the middleware and does not re-check.
func (s *Server) handleFamilyStateFacts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.familyStateListFacts(w, r)
	case http.MethodPost:
		s.familyStateUpsertFact(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) familyStateListFacts(w http.ResponseWriter, r *http.Request) {
	if !s.requireFamilyState(w) {
		return
	}
	opts := familystate.FilterOpts{
		Category: r.URL.Query().Get("category"),
		Subject:  r.URL.Query().Get("subject"),
	}
	facts, err := s.familyState.ListFacts(r.Context(), opts)
	if err != nil {
		jsonErr(w, err, http.StatusInternalServerError)
		return
	}
	jsonOK(w, facts)
}

func (s *Server) familyStateUpsertFact(w http.ResponseWriter, r *http.Request) {
	if !s.requireFamilyState(w) {
		return
	}
	var req familyStateFactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Category == "" || req.Subject == "" || req.Label == "" || req.Value == "" {
		http.Error(w, "category, subject, label, value required", http.StatusBadRequest)
		return
	}
	if len(req.Label) > 64 {
		http.Error(w, "label too long (max 64 chars)", http.StatusBadRequest)
		return
	}
	if len(req.Value) > 512 {
		http.Error(w, "value too long (max 512 chars)", http.StatusBadRequest)
		return
	}
	if !s.validSubject(req.Subject) {
		http.Error(w, "unknown subject", http.StatusBadRequest)
		return
	}

	actor := s.actorName(r.Context())
	f := familystate.Fact{
		Category: req.Category, Subject: req.Subject, Label: req.Label, Value: req.Value,
		CreatedBy: actor,
	}
	if err := s.familyState.UpsertFact(r.Context(), &f); err != nil {
		if errors.Is(err, familystate.ErrUnknownCategory) {
			http.Error(w, "unknown category", http.StatusBadRequest)
			return
		}
		jsonErr(w, err, http.StatusInternalServerError)
		return
	}
	s.auditFamilyState(r.Context(), actor, "family_state_web_upsert_fact",
		map[string]any{"category": req.Category, "subject": req.Subject, "label": req.Label, "id": f.ID})
	jsonOK(w, f)
}

// handleFamilyStateFact dispatches DELETE on /api/family-state/facts/{id}.
func (s *Server) handleFamilyStateFact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireFamilyState(w) {
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/family-state/facts/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.familyState.DeleteFact(r.Context(), id); err != nil {
		jsonErr(w, err, http.StatusInternalServerError)
		return
	}
	s.auditFamilyState(r.Context(), s.actorName(r.Context()), "family_state_web_delete_fact",
		map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

// ── /api/family-state/categories ──────────────────────────────────────────────

// handleFamilyStateCategories dispatches GET (list) and POST (upsert)
// on /api/family-state/categories.
func (s *Server) handleFamilyStateCategories(w http.ResponseWriter, r *http.Request) {
	if !s.requireFamilyState(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		cats, err := s.familyState.ListCategories(r.Context())
		if err != nil {
			jsonErr(w, err, http.StatusInternalServerError)
			return
		}
		jsonOK(w, cats)
	case http.MethodPost:
		var req struct {
			Name         string `json:"name"`
			Description  string `json:"description"`
			AlwaysInject bool   `json:"always_inject"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.Name == "" || req.Description == "" {
			http.Error(w, "name and description required", http.StatusBadRequest)
			return
		}
		if len(req.Name) > 32 || !familyCategoryNameRE.MatchString(req.Name) {
			http.Error(w, "invalid category name — must be [a-z0-9_]+ and ≤ 32 chars", http.StatusBadRequest)
			return
		}
		if len(req.Description) > 256 {
			http.Error(w, "description too long (max 256 chars)", http.StatusBadRequest)
			return
		}
		err := s.familyState.UpsertCategory(r.Context(), &familystate.Category{
			Name: req.Name, Description: req.Description, AlwaysInject: req.AlwaysInject,
		})
		if err != nil {
			if errors.Is(err, familystate.ErrBuiltinCategory) {
				http.Error(w, "cannot modify built-in category via this endpoint", http.StatusBadRequest)
				return
			}
			jsonErr(w, err, http.StatusInternalServerError)
			return
		}
		s.auditFamilyState(r.Context(), s.actorName(r.Context()), "family_state_web_upsert_category",
			map[string]any{"name": req.Name, "always_inject": req.AlwaysInject})
		jsonOK(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleFamilyStateCategory dispatches DELETE on /api/family-state/categories/{name}.
func (s *Server) handleFamilyStateCategory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireFamilyState(w) {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/family-state/categories/")
	if name == "" {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	if err := s.familyState.DeleteCategory(r.Context(), name); err != nil {
		switch {
		case errors.Is(err, familystate.ErrBuiltinCategory):
			http.Error(w, "cannot delete built-in category", http.StatusBadRequest)
		case errors.Is(err, familystate.ErrCategoryNotEmpty):
			http.Error(w, "category has facts; delete them first", http.StatusBadRequest)
		case errors.Is(err, familystate.ErrUnknownCategory):
			http.Error(w, "unknown category", http.StatusNotFound)
		default:
			jsonErr(w, err, http.StatusInternalServerError)
		}
		return
	}
	s.auditFamilyState(r.Context(), s.actorName(r.Context()), "family_state_web_delete_category",
		map[string]any{"name": name})
	w.WriteHeader(http.StatusNoContent)
}

// auditFamilyState writes an audit_log entry. Logging failure is non-fatal —
// the mutation is already committed by the time we get here.
func (s *Server) auditFamilyState(ctx context.Context, actor, toolName string, args map[string]any) {
	if s.db == nil {
		return
	}
	b, _ := json.Marshal(args)
	_ = s.db.LogAudit(ctx, actor, "web", toolName, b)
}
