// crew-control-mcp is an OUT-OF-REPO FamClaw skill addon: a read-only
// HTTP MCP server that exposes firstmate fleet/crew state to the captain's
// famclaw instance (via Discord or Telegram) instead of requiring a terminal.
//
// It is NOT compiled into the famclaw binary. famclaw loads it at runtime:
//   1. SKILL.md  -> installed to <skills.dir>/crew-control/SKILL.md  (prompt injection)
//   2. cmd/crew-control-mcp -> registered as an MCP server under skills.mcp_servers
// See README.md for deployment and config.
module github.com/famclaw/crew-control-mcp

go 1.26.0

require github.com/mark3labs/mcp-go v0.57.0

require (
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/text v0.14.0 // indirect
)
