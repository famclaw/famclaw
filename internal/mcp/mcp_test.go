package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// writeMockServer creates a minimal MCP server script for testing.
// It responds to initialize, tools/list, and tools/call with canned responses.
func writeMockServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	var script string
	var filename string

	if runtime.GOOS == "windows" {
		filename = "mock_mcp.py"
	} else {
		filename = "mock_mcp.py"
	}

	script = `import sys, json

def respond(id, result):
    resp = {"jsonrpc": "2.0", "id": id, "result": result}
    sys.stdout.write(json.dumps(resp) + "\n")
    sys.stdout.flush()

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    req = json.loads(line)
    method = req.get("method", "")
    id = req.get("id", 0)

    if method == "initialize":
        respond(id, {"protocolVersion": "2024-11-05", "serverInfo": {"name": "mock", "version": "1.0"}})
    elif method == "notifications/initialized":
        pass  # notification, no response
    elif method == "tools/list":
        respond(id, {"tools": [
            {"name": "echo", "description": "Echo input", "inputSchema": {}},
            {"name": "add", "description": "Add two numbers", "inputSchema": {}}
        ]})
    elif method == "tools/call":
        name = req["params"]["name"]
        args = req["params"]["arguments"]
        if name == "echo":
            text = args.get("text", "")
            respond(id, {"content": [{"type": "text", "text": text}]})
        elif name == "add":
            a = args.get("a", 0)
            b = args.get("b", 0)
            respond(id, {"content": [{"type": "text", "text": str(a + b)}]})
        else:
            sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": id, "error": {"code": -1, "message": "unknown tool"}}) + "\n")
            sys.stdout.flush()
    else:
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": id, "error": {"code": -32601, "message": "method not found"}}) + "\n")
        sys.stdout.flush()
`

	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func findPython(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skip("python not found, skipping MCP integration test")
	return ""
}

func TestClientStartAndToolsList(t *testing.T) {
	python := findPython(t)
	script := writeMockServer(t)

	client := NewClient(python, script)
	ctx := context.Background()

	if err := client.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer client.Stop()

	tools := client.Tools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Errorf("tool[0] = %q, want echo", tools[0].Name)
	}
	if tools[1].Name != "add" {
		t.Errorf("tool[1] = %q, want add", tools[1].Name)
	}
}

func TestClientCallToolEcho(t *testing.T) {
	python := findPython(t)
	script := writeMockServer(t)

	client := NewClient(python, script)
	ctx := context.Background()
	client.Start(ctx)
	defer client.Stop()

	result, err := client.CallTool(ctx, "echo", map[string]any{"text": "hello world"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	var toolResult ToolCallResult
	if err := json.Unmarshal(result, &toolResult); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(toolResult.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(toolResult.Content))
	}
	if toolResult.Content[0].Text != "hello world" {
		t.Errorf("echo result = %q, want hello world", toolResult.Content[0].Text)
	}
}

func TestClientCallToolAdd(t *testing.T) {
	python := findPython(t)
	script := writeMockServer(t)

	client := NewClient(python, script)
	ctx := context.Background()
	client.Start(ctx)
	defer client.Stop()

	result, err := client.CallTool(ctx, "add", map[string]any{"a": 3, "b": 4})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	var toolResult ToolCallResult
	json.Unmarshal(result, &toolResult)
	if toolResult.Content[0].Text != "7" {
		t.Errorf("add result = %q, want 7", toolResult.Content[0].Text)
	}
}

func TestPoolHasTool(t *testing.T) {
	pool := NewPool()
	if pool.HasTool("nonexistent") {
		t.Error("empty pool should not have any tools")
	}
}

func TestPoolCallToolUnknown(t *testing.T) {
	pool := NewPool()
	_, err := pool.CallTool(context.Background(), "ghost", nil)
	if err == nil {
		t.Error("expected error for unknown tool")
	}
}

func TestPoolRegisterAndStart(t *testing.T) {
	python := findPython(t)
	script := writeMockServer(t)

	pool := NewPool()
	pool.Register(python, script)

	ctx := context.Background()
	if err := pool.StartAll(ctx); err != nil {
		t.Fatalf("StartAll: %v", err)
	}
	defer pool.StopAll()

	if !pool.HasTool("echo") {
		t.Error("pool should have echo tool after start")
	}
	if !pool.HasTool("add") {
		t.Error("pool should have add tool after start")
	}

	tools := pool.ListTools()
	if len(tools) < 2 {
		t.Errorf("expected at least 2 tools, got %d", len(tools))
	}
}

func TestPoolCallTool(t *testing.T) {
	python := findPython(t)
	script := writeMockServer(t)

	pool := NewPool()
	pool.Register(python, script)
	pool.StartAll(context.Background())
	defer pool.StopAll()

	result, err := pool.CallTool(context.Background(), "echo", map[string]any{"text": "pooled"})
	if err != nil {
		t.Fatalf("pool CallTool: %v", err)
	}

	var tr ToolCallResult
	json.Unmarshal(result, &tr)
	if tr.Content[0].Text != "pooled" {
		t.Errorf("pool echo = %q, want pooled", tr.Content[0].Text)
	}
}

func TestMaxToolCallIterations(t *testing.T) {
	if MaxToolCallIterations != 10 {
		t.Errorf("MaxToolCallIterations = %d, want 10", MaxToolCallIterations)
	}
}

func TestJSONRPCTypes(t *testing.T) {
	// Test that types marshal/unmarshal correctly
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params: ToolCallParams{
			Name:      "test",
			Arguments: map[string]any{"key": "value"},
		},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded jsonRPCRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Method != "tools/call" {
		t.Errorf("method = %q", decoded.Method)
	}
}
