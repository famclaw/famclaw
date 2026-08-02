// Command crew-control-mcp runs the crew-control MCP server over HTTP
// (StreamableHTTP transport).
//
// famclaw connects to this server via skills.mcp_servers.<name>.transport=http
// (see config/config.yaml.example). The HTTP transport is used instead of
// stdio because famclaw's landlock+seccomp sandbox wrapping applies only to
// stdio servers; on macOS (where the captain runs famclaw) that sandbox
// cannot be satisfied, and famclaw would refuse to launch a stdio server.
// HTTP servers are plain network calls — no sandbox needed.
//
// The server is READ-ONLY: it exposes fleet_overview, crew_state, and
// backlog, none of which mutate firstmate state. It binds to a specific LAN
// IP (not 0.0.0.0) so the captain controls exactly what network surface is
// exposed.
//
// Usage:
//
//	crew-control-mcp --fm-home /home/dep/tools/firstmate --bind 192.168.1.10 --port 3001
//
// For local testing:
//
//	crew-control-mcp --bind localhost --port 3001
//
// Logs go to stderr; stdout is never used (unlike stdio MCP servers).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/famclaw/crew-control-mcp/internal/crewcrtl"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "crew-control-mcp: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fmHome := flag.String("fm-home", crewcrtl.DefaultFirstMateHome,
		"firstmate home directory (parent of bin/, data/, state/)")
	bind := flag.String("bind", "",
		"LAN IP to bind to (default: auto-detect non-loopback IPv4). "+
			"Use 'localhost' or '127.0.0.1' for local-only binding.")
	port := flag.Int("port", 3001, "TCP port to listen on")
	flag.Parse()

	addr, err := resolveBindAddr(*bind, *port)
	if err != nil {
		return fmt.Errorf("resolving bind address: %w", err)
	}

	cfg := crewcrtl.ClientConfig{FMHome: *fmHome}
	client := crewcrtl.NewClient(cfg)

	mcpSrv := crewcrtl.NewMCPServer(client)

	httpSrv := server.NewStreamableHTTPServer(mcpSrv,
		server.WithEndpointPath("/mcp"),
		server.WithStateLess(true),
		server.WithDisableLocalhostProtection(true),
	)

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("crew-control-mcp: starting HTTP MCP server on %s/mcp", addr)
		log.Printf("crew-control-mcp: fm-home=%s", *fmHome)
		log.Printf("crew-control-mcp: tools=%v", crewcrtl.AllToolNames())
		log.Printf("crew-control-mcp: WARNING — this serves unauthenticated read-only fleet state to any client that can reach %s", addr)
		if err := httpSrv.Start(addr); err != nil && err != http.ErrServerClosed {
			log.Printf("crew-control-mcp: server error: %v", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Println("crew-control-mcp: shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

// resolveBindAddr determines the TCP address to listen on.
// If bind is "localhost" or "127.0.0.1", it uses the loopback address.
// Otherwise, it auto-detects the first non-loopback IPv4 address.
// If bind is empty, auto-detect.
func resolveBindAddr(bind string, port int) (string, error) {
	if bind == "" || bind == "localhost" || bind == "127.0.0.1" {
		host := "127.0.0.1"
		if bind != "" {
			host = bind
		}
		return fmt.Sprintf("%s:%d", host, port), nil
	}

	// Validate a specific IP.
	if ip := net.ParseIP(bind); ip != nil {
		return fmt.Sprintf("%s:%d", ip.String(), port), nil
	}

	// Treat as hostname.
	return fmt.Sprintf("%s:%d", bind, port), nil
}
