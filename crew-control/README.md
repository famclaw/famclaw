# crew-control-mcp

An **out-of-repo**, read-only FamClaw skill addon: an HTTP MCP server that
exposes firstmate fleet/crew state so the captain can ask famclaw "what are
the crews doing?" from Discord or Telegram instead of opening a terminal.

> **Not compiled into famclaw.** famclaw loads this at runtime via:
> 1. `SKILL.md` → installed to `<skills.dir>/crew-control/SKILL.md` (prompt injection)
> 2. `cmd/crew-control-mcp` → registered as an MCP server under `skills.mcp_servers`

## What it does

Three read-only MCP tools, wired into famclaw's existing tool loop:

| Tool             | When to use                                  |
|------------------|----------------------------------------------|
| `fleet_overview` | "What are the crews doing?", "fleet status"  |
| `crew_state`     | "What is X doing?", "status of X"            |
| `backlog`        | "What's in the backlog?", "what's queued"    |

That is the entire scope. **No start, stop, steer, or teardown tools** —
those touch real infrastructure and are a separate, later decision.

## How the wiring works

1. **SKILL.md** — installed to `<skills.dir>/crew-control/SKILL.md`. famclaw's
   `skillbridge` parses it and injects its body into the system prompt as an
   `<AgentSkills>` block, telling the model which tools to call.
2. **cmd/crew-control-mcp** — an HTTP MCP server. famclaw registers it under
   `skills.mcp_servers` with `transport: http` and connects over the LAN.
3. **The tool name in SKILL.md == the MCP tool name** — the model reads the
   prompt and calls `crew_state`; the agent's tool loop routes it to
   `mcp.Pool.CallTool("crew_state", …)`, which reaches this server.

## Why HTTP, not stdio

famclaw's landlock+seccomp sandbox wrapping (verified in
`internal/mcp/client.go:102-145`) applies **only** to `case "stdio"` in
`Client.Start()`. On macOS — where the captain runs famclaw —
`checkLandlockSupport()` and `checkSeccompSupport()` both fail, and famclaw
**refuses** to launch a stdio MCP server unless
`tools.sandbox.allow_unconfined: true`.

HTTP servers skip the sandbox path entirely — they are plain network calls.
famclaw already talks to an HTTP MCP server this way
(`skills.mcp_servers.inventory`, `transport: http`).

Using HTTP also puts the server on the Linux box where the fleet state
actually lives.

## Install

### 1. Build the MCP server binary

On the Linux box (CGO_ENABLED=0, like the rest of famclaw):

```bash
cd crew-control
CGO_ENABLED=0 go build -o /opt/crew-control-mcp ./cmd/crew-control-mcp
```

### 2. Install the skill

```bash
mkdir -p ~/.famclaw/skills/crew-control
cp SKILL.md ~/.famclaw/skills/crew-control/SKILL.md
```

### 3. Configure famclaw (`config.yaml`)

```yaml
skills:
  dir: ~/.famclaw/skills

  mcp_servers:
    crew-control:
      transport: http
      url: "http://192.168.1.10:3001/mcp"

  role_enablement:
    parent:
      - crew-control
    # child: intentionally NOT enabled — children never see fleet state
```

See `config/config.yaml.example` for a full annotated example.

### 4. Start the server on the Linux box

```bash
/opt/crew-control-mcp --bind 192.168.1.10 --port 3001
```

- `--bind` must be a specific LAN IP, **not** `0.0.0.0`. This limits exposure to the LAN.
- `--fm-home` defaults to `/home/dep/tools/firstmate` (override if your firstmate
  home is elsewhere).

## Test

```bash
# From the skill directory (tests run against the live firstmate home):
CGO_ENABLED=0 go test ./...

# Cross-compile for your platform:
CGO_ENABLED=0 go build ./...
```

## Security notes

- **Bind to the LAN IP, not 0.0.0.0** — the server serves unauthenticated
  read-only fleet state. Any device that can reach the port can call the
  tools. This is acceptable in a trusted home LAN but not in a hostile
  network.
- **Crew ID validation** — every `crew_id` is validated against
  `^[a-zA-Z0-9][a-zA-Z0-9_-]*$` before any script is invoked. IDs are passed
  to `exec.CommandContext` (no shell), so `;`, backticks, `$()`, `../`, and
  similar metacharacters are rejected outright.
- **No write path** — the tool list contains exactly three tools, all
  read-only. No mutation is possible.
- **Parent-only** — the skill is gated via `role_enablement` so only the
  parent role gets it; children receive a standard "not something I can help
  with" reply.
