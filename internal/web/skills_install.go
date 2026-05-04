package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// skillNameRe matches a safe single skill name: starts with an alnum, then
// alnum dot underscore or dash. Forbids path separators, leading dot, and
// traversal segments — anything that filepath.Join could interpret as a
// directory boundary.
var skillNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// skillRefRe matches a safe install reference: either a single skill name
// or an "org/repo" pair (one slash). Both halves obey skillNameRe rules.
// This is intentionally strict — the dashboard form is for the common
// "famclaw/seccheck" case. Operators installing from a local checkout
// should use the CLI (`famclaw skill install /abs/path`), which has
// shell-level access controls already.
var skillRefRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}(/[A-Za-z0-9][A-Za-z0-9._-]{0,127})?$`)

// validateSkillName guards Remove against path traversal — body.Name flows
// straight into filepath.Join(r.dir, name) inside the registry, so a value
// like "../etc" would let a parent-PIN holder os.RemoveAll arbitrary
// directories.
func validateSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if !skillNameRe.MatchString(name) {
		return fmt.Errorf("name must match %s", skillNameRe.String())
	}
	return nil
}

// validateSkillRef sanitizes the install reference at the HTTP boundary.
// Strict regex match — no absolute paths, no '..', no leading dot, no
// path separators beyond a single org/repo slash.
func validateSkillRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("name_or_path is required")
	}
	if !skillRefRe.MatchString(ref) {
		return fmt.Errorf("name_or_path must match %s (e.g. 'famclaw/seccheck')", skillRefRe.String())
	}
	return nil
}

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
	ref := strings.TrimSpace(body.NameOrPath)
	if err := validateSkillRef(ref); err != nil {
		jsonErr(w, err, http.StatusBadRequest)
		return
	}
	skill, err := s.skillRegistry.Install(r.Context(), ref)
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
	name := strings.TrimSpace(body.Name)
	if err := validateSkillName(name); err != nil {
		jsonErr(w, err, http.StatusBadRequest)
		return
	}
	if err := s.skillRegistry.Remove(name); err != nil {
		jsonErr(w, err, http.StatusInternalServerError)
		return
	}
	go s.broadcastDashboardUpdate(context.Background())
	jsonOK(w, map[string]string{"status": "removed", "name": name})
}
