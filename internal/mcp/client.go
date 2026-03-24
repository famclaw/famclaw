package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"sync/atomic"
)

// Client manages a single MCP server process over stdio JSON-RPC.
type Client struct {
	cmd     string
	args    []string
	process *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	mu      sync.Mutex
	nextID  atomic.Int64
	tools   []Tool
	running bool
}

// NewClient creates an MCP client that will spawn the given command.
// The process is started lazily on first call.
func NewClient(cmd string, args ...string) *Client {
	return &Client{cmd: cmd, args: args}
}

// Start spawns the MCP server process and performs the initialize handshake.
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return nil
	}

	c.process = exec.CommandContext(ctx, c.cmd, c.args...)
	stdin, err := c.process.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdout, err := c.process.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := c.process.Start(); err != nil {
		return fmt.Errorf("starting MCP server %q: %w", c.cmd, err)
	}

	c.stdin = stdin
	c.scanner = bufio.NewScanner(stdout)
	c.scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer
	c.running = true

	// Initialize handshake
	_, err = c.call(ctx, "initialize", InitializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo:      ClientInfo{Name: "famclaw", Version: "1.0"},
	})
	if err != nil {
		c.stop()
		return fmt.Errorf("MCP initialize: %w", err)
	}

	// Send initialized notification (no response expected — but we send as request for simplicity)
	c.sendNotification("notifications/initialized", nil)

	// List available tools
	result, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		log.Printf("[mcp] tools/list failed: %v (server may have no tools)", err)
	} else {
		var toolsList ToolsListResult
		if err := json.Unmarshal(result, &toolsList); err == nil {
			c.tools = toolsList.Tools
		}
	}

	return nil
}

// CallTool invokes a tool on the MCP server.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		if err := c.Start(ctx); err != nil {
			return nil, err
		}
		c.mu.Lock()
	}
	c.mu.Unlock()

	result, err := c.call(ctx, "tools/call", ToolCallParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return nil, fmt.Errorf("calling tool %q: %w", name, err)
	}
	return result, nil
}

// Tools returns the list of available tools.
func (c *Client) Tools() []Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tools
}

// Stop terminates the MCP server process.
func (c *Client) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stop()
}

func (c *Client) stop() {
	if !c.running {
		return
	}
	c.running = false
	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.process != nil && c.process.Process != nil {
		c.process.Process.Kill()
		c.process.Wait() //nolint:errcheck
	}
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := int(c.nextID.Add(1))

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	c.mu.Lock()
	_, err = fmt.Fprintf(c.stdin, "%s\n", data)
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("writing to MCP server: %w", err)
	}

	// Read response (blocking)
	for c.scanner.Scan() {
		line := c.scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp jsonRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue // skip non-JSON lines (e.g. server logs)
		}
		if resp.ID == id {
			if resp.Error != nil {
				return nil, fmt.Errorf("RPC error %d: %s", resp.Error.Code, resp.Error.Message)
			}
			return resp.Result, nil
		}
		// Not our response — could be a notification, skip it
	}

	if err := c.scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading from MCP server: %w", err)
	}
	return nil, fmt.Errorf("MCP server closed connection")
}

func (c *Client) sendNotification(method string, params any) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      0, // notifications shouldn't have ID, but we use 0 for simplicity
		Method:  method,
		Params:  params,
	}
	data, _ := json.Marshal(req)
	c.mu.Lock()
	fmt.Fprintf(c.stdin, "%s\n", data) //nolint:errcheck
	c.mu.Unlock()
}
