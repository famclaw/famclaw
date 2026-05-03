package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
)

// skillNameRe matches a safe skill name: starts with an alnum, then alnum
// dot underscore or dash. Forbids path separators, leading dot, traversal
// segments, and any character that filepath.Join could interpret as a
// directory boundary.
var skillNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// validateSkillName guards Remove against path traversal — body.Name flows
// straight into filepath.Join(r.dir, name) inside the registry, so a value
// like "../etc" would let a parent-PIN holder os.RemoveAll arbitrary
// directories. Defense in depth: the registry should also validate, but
// gating it at the HTTP boundary is the cheaper fix.
func validateSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if !skillNameRe.MatchString(name) {
		return fmt.Errorf("name must match %s", skillNameRe.String())
	}
	return nil
}

// validateSkillRef sanitizes the install path/ref. We reject ".." segments
// after Clean — that catches both "../../etc" and "foo/../../etc" — and we
// reject NUL bytes for good measure. Absolute paths are allowed (a parent
// installing a locally-checked-out skill repo is a legitimate use case),
// but they must not contain traversal.
func validateSkillRef(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("name_or_path is required")
	}
	if strings.ContainsRune(ref, 0) {
		return "", fmt.Errorf("name_or_path must not contain NUL bytes")
	}
	if len(ref) > 1024 {
		return "", fmt.Errorf("name_or_path too long")
	}
	cleaned := filepath.Clean(ref)
	for _, part := range strings.Split(filepath.ToSlash(cleaned), "/") {
		if part == ".." {
			return "", fmt.Errorf("name_or_path must not contain '..' segments")
		}
	}
	return cleaned, nil
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
	cleaned, err := validateSkillRef(strings.TrimSpace(body.NameOrPath))
	if err != nil {
		jsonErr(w, err, http.StatusBadRequest)
		return
	}
	skill, err := s.skillRegistry.Install(r.Context(), cleaned)
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
