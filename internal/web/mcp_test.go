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
)

func newMCPSignalTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	parent := config.UserConfig{
		Name:        "sarah",
		DisplayName: "Sarah",
		Role:        "parent",
		PIN:         "1234",
	}
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	// Seed an empty config file so the write target exists.
	if err := os.WriteFile(cfgPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("seed config file: %v", err)
	}
	return &Server{
		cfg:     &config.Config{Users: []config.UserConfig{parent}},
		cfgPath: cfgPath,
		cfgMu:   sync.RWMutex{},
	}, cfgPath
}

func TestMCP_ListEmpty(t *testing.T) {
	s, _ := newMCPSignalTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/mcp", nil)
	rec := httptest.NewRecorder()

	s.handleMCP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status OK, got %d", rec.Code)
		return
	}
	var resp map[string][]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if len(resp["servers"]) != 0 {
		t.Errorf("expected empty servers list, got %v", resp["servers"])
	}
}

func TestMCP_ListWithServers(t *testing.T) {
	s, _ := newMCPSignalTestServer(t)
	s.cfgMu.Lock()
	s.cfg.Skills.MCPServers = map[string]config.MCPServerConfig{
		"foo": {Transport: "stdio", Command: "foo"},
		"bar": {Transport: "http", URL: "http://example.com"},
	}
	s.cfgMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/mcp", nil)
	rec := httptest.NewRecorder()

	s.handleMCP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status OK, got %d", rec.Code)
		return
	}
	var resp map[string][]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	// Expect sorted list: bar, foo
	expected := []string{"bar", "foo"}
	if len(resp["servers"]) != len(expected) {
		t.Errorf("expected %d servers, got %d", len(expected), len(resp["servers"]))
	}
	for i, v := range expected {
		if resp["servers"][i] != v {
			t.Errorf("server[%d]: expected %s, got %s", i, v, resp["servers"][i])
		}
	}
}

func TestMCPAdd_MethodGate(t *testing.T) {
	s, _ := newMCPSignalTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/mcp/add", bytes.NewReader([]byte{}))
	rec := httptest.NewRecorder()

	s.handleMCPAdd(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed, got %d", rec.Code)
	}
}

func TestMCPAdd_EmptyBody(t *testing.T) {
	s, _ := newMCPSignalTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/add", bytes.NewReader([]byte{}))
	rec := httptest.NewRecorder()

	s.handleMCPAdd(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", rec.Code)
	}
}

func TestMCPAdd_MissingName(t *testing.T) {
	s, _ := newMCPSignalTestServer(t)
	body, _ := json.Marshal(map[string]interface{}{
		"name":   "",
		"config": map[string]string{"transport": "stdio", "command": "test"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/add", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleMCPAdd(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", rec.Code)
	}
}

func TestMCPAdd_InvalidConfig(t *testing.T) {
	s, _ := newMCPSignalTestServer(t)
	body, _ := json.Marshal(map[string]interface{}{
		"name":   "test",
		"config": map[string]string{"transport": "stdio"}, // missing command
	})
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/add", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleMCPAdd(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", rec.Code)
	}
}

func TestMCPAdd_HappyPath(t *testing.T) {
	s, _ := newMCPSignalTestServer(t)
	body, _ := json.Marshal(map[string]interface{}{
		"name": "test",
		"config": map[string]interface{}{
			"transport": "stdio",
			"command":   "echo",
			"args":      []string{"hello"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/add", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleMCPAdd(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", rec.Code)
		return
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if resp["status"] != "saved" {
		t.Errorf("expected status 'saved', got %s", resp["status"])
	}

	// Verify config updated
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if s.cfg.Skills.MCPServers == nil {
		t.Fatal("MCPServers map is nil")
	}
	if cfg, ok := s.cfg.Skills.MCPServers["test"]; !ok {
		t.Errorf("expected server 'test' not found")
	} else {
		if cfg.Command != "echo" {
			t.Errorf("expected command 'echo', got %s", cfg.Command)
		}
		if len(cfg.Args) != 1 || cfg.Args[0] != "hello" {
			t.Errorf("expected args ['hello'], got %v", cfg.Args)
		}
	}
}

func TestMCPRemove_MethodGate(t *testing.T) {
	s, _ := newMCPSignalTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/mcp/remove", bytes.NewReader([]byte{}))
	rec := httptest.NewRecorder()

	s.handleMCPRemove(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed, got %d", rec.Code)
	}
}

func TestMCPRemove_MissingName(t *testing.T) {
	s, _ := newMCPSignalTestServer(t)
	body, _ := json.Marshal(map[string]string{"name": ""})
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/remove", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleMCPRemove(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", rec.Code)
	}
}

func TestMCPRemove_HappyPath(t *testing.T) {
	s, _ := newMCPSignalTestServer(t)
	s.cfgMu.Lock()
	s.cfg.Skills.MCPServers = map[string]config.MCPServerConfig{
		"test": {Transport: "stdio", Command: "test"},
	}
	s.cfgMu.Unlock()

	body, _ := json.Marshal(map[string]string{"name": "test"})
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/remove", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleMCPRemove(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", rec.Code)
		return
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if resp["status"] != "removed" {
		t.Errorf("expected status 'removed', got %s", resp["status"])
	}
	if resp["name"] != "test" {
		t.Errorf("expected name 'test', got %s", resp["name"])
	}

	// Verify config updated
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if s.cfg.Skills.MCPServers != nil {
		if _, ok := s.cfg.Skills.MCPServers["test"]; ok {
			t.Errorf("expected server 'test' to be removed")
		}
	}
}

func TestMCPRemove_NonExistent(t *testing.T) {
	s, _ := newMCPSignalTestServer(t)
	body, _ := json.Marshal(map[string]string{"name": "nonexistent"})
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/remove", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleMCPRemove(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", rec.Code)
		return
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if resp["status"] != "removed" {
		t.Errorf("expected status 'removed', got %s", resp["status"])
	}
	if resp["name"] != "nonexistent" {
		t.Errorf("expected name 'nonexistent', got %s", resp["name"])
	}
}
