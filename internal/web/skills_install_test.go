package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/skillbridge"
)

func newSkillTestServer(t *testing.T) *Server {
	t.Helper()
	parent := config.UserConfig{
		Name:        "sarah",
		DisplayName: "Sarah",
		Role:        "parent",
		PIN:         testParentPIN,
	}
	reg := skillbridge.NewRegistry(t.TempDir(), nil, skillbridge.InstallConfig{}, nil)
	return &Server{
		cfg:           &config.Config{Users: []config.UserConfig{parent}},
		cfgMu:         sync.RWMutex{},
		skillRegistry: reg,
	}
}

// TestSkillInstall_MethodGate locks in that the handler still rejects non-POST
// requests. The PIN gate that previously lived in this handler is gone — auth
// is now enforced by the session middleware on the protected route, and is
// covered by the auth/middleware tests rather than here.
func TestSkillInstall_MethodGate(t *testing.T) {
	s := newSkillTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/skills/install", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()
	s.handleSkillInstall(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestSkillInstall_EmptyBody(t *testing.T) {
	s := newSkillTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/skills/install", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()
	s.handleSkillInstall(rec, req)
	if rec.Code != 400 {
		t.Errorf("want 400, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestSkillInstall_HappyPath(t *testing.T) {
	s := newSkillTestServer(t)
	// The handler accepts org/repo style refs and resolves them relative
	// to cwd (skillbridge.Registry.Install opens "<ref>/SKILL.md"). Chdir
	// into a temp dir so the test owns the resolution root.
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join("famclaw", "testskill"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := `---
name: testskill
description: a test
---
body
`
	if err := os.WriteFile(filepath.Join("famclaw", "testskill", "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	bodyJSON, _ := json.Marshal(map[string]string{"name_or_path": "famclaw/testskill"})
	req := httptest.NewRequest(http.MethodPost, "/api/skills/install", bytes.NewReader(bodyJSON))
	rec := httptest.NewRecorder()
	s.handleSkillInstall(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["name"] != "testskill" {
		t.Errorf("want name=testskill, got %v", resp["name"])
	}
}

func TestSkillInstall_RejectsPathTraversal(t *testing.T) {
	s := newSkillTestServer(t)
	cases := []string{
		"../etc",
		"foo/../../etc",
		"\x00malicious",
		"/abs/path",
		".hidden",
		"a/b/c",
	}
	for _, ref := range cases {
		t.Run(ref, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"name_or_path": ref})
			req := httptest.NewRequest(http.MethodPost, "/api/skills/install", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			s.handleSkillInstall(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("want 400, got %d (body: %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSkillRemove_RejectsBadName(t *testing.T) {
	s := newSkillTestServer(t)
	cases := []string{
		"../etc",
		"foo/bar",
		"..",
		".hidden",
		"",
		"name with space",
		"name\x00",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"name": name})
			req := httptest.NewRequest(http.MethodPost, "/api/skills/remove", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			s.handleSkillRemove(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("want 400, got %d (body: %s)", rec.Code, rec.Body.String())
			}
		})
	}
}
