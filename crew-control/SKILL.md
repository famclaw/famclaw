---
name: crew-control
description: Read-only crew and fleet state from the firstmate dashboard.
version: "0.1"
author: famclaw
license: AGPL-3.0-only
tags: [devops, family, fleet, crew, read-only]
platforms: [linux, darwin]
requires:
  bins: [fm-fleet-view.sh, fm-crew-state.sh]
trigger:
  mode: "keyword"
  keywords: ["crew", "fleet", "backlog", "what are the crews", "status", "what's happening", "what is happening", "crews doing", "in flight", "queued"]
---
# Crew Control Skill

This skill exposes a **read-only** HTTP MCP server that wraps firstmate's
fleet scripts so the captain can ask famclaw "what are the crews doing?"
from Discord or Telegram — without opening a terminal.

## Tools

### fleet_overview

Get a whole-fleet overview from the firstmate fleet view. Shows all crews
(in-flight, queued, done) with their current state, backend, endpoint,
artifact, and watch channel.

Call this when the captain asks:
- "What are the crews doing?"
- "Show me the fleet status"
- "What's happening across all crews?"

No arguments.

### crew_state

Get the current state of a single crew by its firstmate crew ID. Returns one
line: `state: <state> · source: <source> · <detail>`.

Call this when the captain asks about a specific crew:
- "What is fc-crew-control-mcp doing?"
- "Status of todo-add-skill"
- "Is fc-sandbox-per-convo still working?"

Arguments:
- `crew_id` (required) — the firstmate crew ID, e.g. `fc-crew-control-mcp`.
  Must be a simple identifier (letters, digits, dashes, underscores). Shell
  metacharacters are rejected before any script runs.

### backlog

Show the in-flight and queued backlog items from the firstmate backlog.

Call this when the captain asks:
- "What's in the backlog?"
- "What's queued?"
- "Show me what's in flight"

No arguments.

## Workflow

1. **Overview** — when the captain asks "what are the crews doing?", call
   `fleet_overview` for the whole-fleet Markdown view.
2. **Drill in** — when the captain asks about a specific crew, call
   `crew_state` with that crew's ID to get the precise current state.
3. **Backlog** — when the captain asks about queued or upcoming work, call
   `backlog`.

## Scope

This skill is **READ-ONLY**. It exposes exactly three tools:

| Tool             | Reads from               | Mutates? |
|------------------|--------------------------|----------|
| `fleet_overview` | `bin/fm-fleet-view.sh`   | No       |
| `crew_state`     | `bin/fm-crew-state.sh`   | No       |
| `backlog`        | `data/backlog.md`        | No       |

**No start, stop, steer, or teardown tools exist.** Those operations touch
real infrastructure and are a separate, later decision. This skill will
never be extended with write capabilities without an explicit new decision
from the captain.

## Access control

Only the **parent** role gets this skill. Children never see fleet state —
when a child asks, the model gives a normal "that's not something I can help
with" reply rather than confirming or denying what exists. This is enforced
via `role_enablement` in famclaw's `SkillsConfig` (see config example below).

## Security

- The MCP server runs on the **Linux box** where the fleet state lives,
  reached by famclaw over HTTP (not stdio). HTTP is used instead of stdio
  because famclaw's landlock+seccomp sandbox wrapping applies only to stdio
  servers; on macOS (where the captain runs famclaw) that sandbox cannot be
  satisfied, and famclaw refuses to launch a stdio server unless
  `tools.sandbox.allow_unconfined: true`. HTTP servers are plain network
  calls — no sandbox needed.
- The server binds to a specific LAN IP (not `0.0.0.0`). See `report.md`
  for the exact address used and what it exposes.
- The server serves **unauthenticated** fleet state. Any device that can
  reach the port can call the tools. This is acceptable in a trusted home
  LAN but would NOT be acceptable in a hostile or multi-tenant network.
- Crew IDs are validated against a strict identifier regex
  (`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`) before any script is invoked. IDs are
  passed to `exec.CommandContext` (which never spawns a shell), so shell
  metacharacters, path traversal, and command substitution cannot reach a
  shell under any circumstance.

## Configuration

See `config/config.yaml.example` for the exact famclaw config block.

The MCP server binary is built and placed on the Linux box:
```bash
cd crew-control
CGO_ENABLED=0 go build -o /opt/crew-control-mcp ./cmd/crew-control-mcp
```

Start it on the Linux box (bind to the LAN IP, not 0.0.0.0):
```bash
/opt/crew-control-mcp --bind 192.168.1.10 --port 3001
```
