// trello-skill is an OUT-OF-REPO FamClaw skill addon.
// It is a separate Go module: NOT compiled into the famclaw binary.
// famclaw loads it at runtime via:
//   1. SKILL.md  -> installed to <skills.dir>/trello/SKILL.md (prompt injection)
//   2. cmd/trello-mcp -> registered as an MCP server under skills.mcp_servers
// See README.md for deployment and config.
module github.com/famclaw/trello-skill

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
