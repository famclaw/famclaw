// Command trello-mcp runs the Trello skill as an MCP server over stdio.
//
// famclaw launches this binary as a stdio MCP server (registered under
// skills.mcp_servers in config.yaml). Credentials are injected by famclaw
// from skills.credentials into the process environment — never hardcode them.
//
// Usage:
//
//	trello-mcp
//
// Stdin/stdout carry the MCP JSON-RPC (Content-Length framed) protocol; logs
// go to stderr so stdout stays clean for MCP framing.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/famclaw/trello-skill/internal/trello"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "trello-mcp: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	creds := trello.LoadCredentials()
	if len(creds.Lists) > 0 {
		logf("trello-mcp: list name resolution enabled (%d lists from TRELLO_LISTS)", len(creds.Lists))
	} else {
		logf("trello-mcp: WARNING no TRELLO_LISTS configured — only 24-char hex list ids will be accepted for list_id/person")
	}
	if creds.ListID == "" {
		logf("trello-mcp: WARNING no TRELLO_LIST_ID configured — cards with no person will fail")
	}
	srv := trello.NewServer(creds)

	logf("trello-mcp: starting server (name=trello-skill)")
	stdio := server.NewStdioServer(srv)
	if err := stdio.Listen(ctx, os.Stdin, os.Stdout); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return nil
}

// logf writes a log line to stderr, keeping stdout clean for MCP framing.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
