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
	"github.com/famclaw/famclaw/internal/mcp"
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
	var resp struct {
		Servers []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Tools  int    `json:"tools"`
		} `json:"servers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if len(resp.Servers) != 0 {
		t.Errorf("expected empty servers list, got %v", resp.Servers)
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
	var resp struct {
		Servers []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Tools  int    `json:"tools"`
		} `json:"servers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	// Expect sorted list: bar, foo
	expected := []string{"bar", "foo"}
	if len(resp.Servers) != len(expected) {
		t.Errorf("expected %d servers, got %d", len(expected), len(resp.Servers))
	}
	for i, v := range expected {
		if resp.Servers[i].Name != v {
			t.Errorf("server[%d]: expected %s, got %s", i, v, resp.Servers[i].Name)
		}
		if resp.Servers[i].Status != "pending" {
			t.Errorf("server %s status = %q, want pending", v, resp.Servers[i].Status)
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
	var resp map[string]any
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
	var resp map[string]any
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
	var resp map[string]any
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

func TestMCPAddReturnsPoolFailureStatus(t *testing.T) {
	s, _ := newMCPSignalTestServer(t)
	s.pool = mcp.NewPool(t.TempDir(), false, false)
	body, err := json.Marshal(map[string]any{"name": "broken", "config": map[string]any{"transport": "stdio", "command": "/not/a/real/mcp-server"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/add", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleMCPAdd(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		PoolStatus string `json:"pool_status"`
		Error      string `json:"error"`
		Tools      int    `json:"tools"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.PoolStatus != "failed" || response.Error == "" {
		t.Fatalf("unexpected response: %#v", response)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/mcp", nil)
	rec = httptest.NewRecorder()
	s.handleMCP(rec, req)
	var list struct {
		Servers []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"servers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Servers) != 1 || list.Servers[0].Name != "broken" || list.Servers[0].Status != "failed" {
		t.Fatalf("unexpected server states: %#v", list)
	}
}

func TestMCPAddFailsClosedWithoutScanner(t *testing.T) {
	s, _ := newMCPSignalTestServer(t)
	s.cfg.SecCheck.Enabled = true
	body, _ := json.Marshal(map[string]any{"name": "blocked", "config": map[string]any{"transport": "stdio", "command": "echo"}})
	rec := httptest.NewRecorder()
	s.handleMCPAdd(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/add", bytes.NewReader(body)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if _, exists := s.cfg.Skills.MCPServers["blocked"]; exists {
		t.Fatal("server config mutated despite unavailable scanner")
	}
}
