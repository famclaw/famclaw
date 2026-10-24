// Command android-control is the android-control MCP server.
//
// It serves the Android device-control tool surface (design doc §4) over
// stdio JSON-RPC so the famclaw mcp.Pool can spawn it as a local MCP
// server. All logging goes to stderr; stdout is reserved for the MCP
// JSON-RPC stream (project rule 9).
//
// Startup:
//
//	ANDROID_CONTROL_POLICY_FILE=/path/to/android-control.yaml android-control
//
// When the env var is unset the server starts in stub mode (safe defaults:
// shell off, screen policy refuse_when_off, empty allowlist) so it can be
// registered and exercised before a policy file is provisioned.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/famclaw/famclaw/internal/mcpserver"
	mcpgo_server "github.com/mark3labs/mcp-go/server"
)

// version is stamped at build time; keep in sync with the mcp.yaml
// registration when it lands.
const version = "0.1.0"

func main() {
	// stdout belongs to MCP JSON-RPC — logs go to stderr.
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	var policyPath string
	flag.StringVar(&policyPath, "policy", os.Getenv("ANDROID_CONTROL_POLICY_FILE"),
		"path to android-control.yaml policy file (default: $ANDROID_CONTROL_POLICY_FILE, stub mode if unset)")
	flag.Parse()

	policy, err := mcpserver.LoadPolicyConfig(policyPath)
	if err != nil {
		log.Fatalf("loading android-control policy: %v", err)
	}

	srv := mcpserver.New(policy, version)
	log.Printf("android-control: server ready (policy=%q device=%s shell_enabled=%v apps=%d)",
		orDash(policyPath), policy.DeviceAlias, policy.Tools != nil && policy.Tools["shell"], len(policy.Apps))

	if err := mcpgo_server.ServeStdio(srv); err != nil && err != fmt.Errorf("server has not been initialized") {
		log.Fatalf("serving MCP stdio: %v", err)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
