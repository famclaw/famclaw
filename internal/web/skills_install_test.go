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

const testParentPIN = "1234"

func newSkillTestServer(t *testing.T) *Server {
	t.Helper()
	parent := config.UserConfig{
		Name:        "sarah",
		DisplayName: "Sarah",
		Role:        "parent",
		PIN:         testParentPIN,
	}
	reg := skillbridge.NewRegistry(t.TempDir(), nil, skillbridge.InstallConfig{})
	return &Server{
		cfg:           &config.Config{Users: []config.UserConfig{parent}},
		cfgMu:         sync.RWMutex{},
		skillRegistry: reg,
	}
}

func TestSkillInstall_PINGate(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		pin       string
		setHeader bool
		wantCode  int
	}{
		{"no PIN", http.MethodPost, "", false, 403},
		{"wrong PIN", http.MethodPost, "0000", true, 403},
		{"GET not allowed", http.MethodGet, testParentPIN, true, 405},
	}

	s := newSkillTestServer(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/skills/install", bytes.NewReader([]byte("{}")))
			if tc.setHeader {
				req.Header.Set("X-Parent-PIN", tc.pin)
			}
			rec := httptest.NewRecorder()
			s.handleSkillInstall(rec, req)
			if rec.Code != tc.wantCode {
				t.Errorf("want %d, got %d (body: %s)", tc.wantCode, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSkillInstall_EmptyBody(t *testing.T) {
	s := newSkillTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/skills/install", bytes.NewReader([]byte("{}")))
	req.Header.Set("X-Parent-PIN", testParentPIN)
	rec := httptest.NewRecorder()
	s.handleSkillInstall(rec, req)
	if rec.Code != 400 {
		t.Errorf("want 400, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestSkillInstall_HappyPath(t *testing.T) {
	s := newSkillTestServer(t)
	tempDir := t.TempDir()
	skillDir := filepath.Join(tempDir, "testskill")
	if err := os.Mkdir(skillDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	content := `---
name: testskill
description: a test
---
body
`
	if err := os.WriteFile(skillPath, []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	bodyJSON, _ := json.Marshal(map[string]string{"name_or_path": skillDir})
	req := httptest.NewRequest(http.MethodPost, "/api/skills/install", bytes.NewReader(bodyJSON))
	req.Header.Set("X-Parent-PIN", testParentPIN)
	rec := httptest.NewRecorder()
	s.handleSkillInstall(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["Name"] != "testskill" {
		t.Errorf("want Name=testskill, got %v", resp["Name"])
	}
}
