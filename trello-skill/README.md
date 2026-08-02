# trello-skill

An **out-of-repo** FamClaw skill addon: a Trello-backed todo list with per-person
lists.

> **Not compiled into famclaw.** famclaw loads this at runtime via its SKILL.md
> (prompt injection) and this bundle's `cmd/trello-mcp` MCP server. No `go
> build` of famclaw is required to add or change the skill — it lives entirely
> in this separate module.

## What it does

Three MCP tools, wired into famclaw's existing tool loop. Each family member has
their own list on a shared board; tasks are routed by person:

| Tool                | When to use                                  |
|---------------------|----------------------------------------------|
| `trello_add_card`   | "Add this to the todo", "remember to X", "task for Julia" |
| `trello_list_cards` | "What's on my todo?", "show my tasks", "list Julia's cards" |
| `trello_complete_card` | "I finished X", "mark X done"              |

**Per-person routing.** `trello_add_card` takes a `person` argument. The model
passes a family member's name (e.g. `Julia`) and the skill resolves it to that
person's list — the card lands in Julia's list, not the shared Backlog. Omit
`person` to use the default list (Backlog). Names and list ids come from
`TRELLO_LISTS` in config — never hardcoded.

**Idempotency.** Before creating a card, the skill checks whether an open card
with the same title already exists on the target list. If it does, the existing
card is returned (with its id) instead of creating a duplicate. This prevents
the retry spiral that produced duplicate cards.

**No silent failures.** `list_id`/`person` is validated (must be a configured
name or a 24-char hex id) before any API call. A bad value returns a clear
error naming the valid lists — it is never passed silently to Trello.

## How the wiring works

1. **SKILL.md** — installed to `<skills.dir>/trello/SKILL.md`. famclaw's
   `skillbridge` parses it and injects its body into the system prompt as an
   `<AgentSkills>` block, telling the model which tools to call.
2. **cmd/trello-mcp** — a stdio MCP server. famclaw registers it under
   `skills.mcp_servers` and its `mcp.Pool` exposes the tool names to the agent.
3. **The tool name in SKILL.md == the MCP tool name** — the model reads the
   prompt and calls `trello_add_card`; the agent's tool loop routes it to
   `mcp.Pool.CallTool("trello_add_card", …)`, which reaches this server.

Credentials are injected by famclaw from `skills.credentials` into the MCP
subprocess environment. They never live in the skill file or this repo.

## Install

```bash
# 1. Build the MCP server binary (CGO_ENABLED=0, like the rest of famclaw).
cd trello-skill
CGO_ENABLED=0 go build -o ~/.famclaw/bin/trello-mcp ./cmd/trello-mcp

# 2. Copy the skill into your out-of-repo skills dir.
mkdir -p ~/.famclaw/skills/trello
cp SKILL.md ~/.famclaw/skills/trello/SKILL.md
```

## Configure famclaw (`config.yaml`)

```yaml
skills:
  dir: ~/.famclaw/skills          # out-of-repo skills directory

  mcp_servers:
    trello:
      transport: stdio
      # famclaw launches MCP servers via fork/exec, which does NOT expand "~".
      # Use an ABSOLUTE path, or the server fails with "no such file or
      # directory". Replace /Users/family with your home directory.
      command: /Users/family/.famclaw/bin/trello-mcp

  credentials:                    # ALL secrets go here — never in SKILL.md
    trello:
      TRELLO_API_KEY: "YOUR-KEY"
      TRELLO_TOKEN: "YOUR-TOKEN"
      TRELLO_BOARD_ID: "YOUR-BOARD-ID"
      TRELLO_LIST_ID: "YOUR-BACKLOG-LIST-ID"   # default list for new cards
      TRELLO_DONE_LIST_ID: "YOUR-DONE-LIST-ID"  # list completed cards move to
      # Name -> list-id map. Drives name resolution and per-person routing.
      # Use double quotes inside the JSON (it is a single YAML string).
      TRELLO_LISTS: '{"Backlog":"68e...","Julia":"68e...","Ilya":"68e...","Teo":"68e...","Done":"68e..."}'

  role_enablement:
    parent: [trello]
    child: [trello]
```

A complete example is in `config/config.yaml.example`.

## Routing & validation

- **`person` (add_card)** — set to a family member's name (e.g. `Julia`) to
  route the card to their list. You know who you're talking to, so use that
  name for the speaker's own tasks. Omit for the default Backlog list.
- **`list_id` (list_cards)** — a list name (e.g. `Julia`, `Done`) or a
  24-char hex id.
- Both are resolved against the `TRELLO_LISTS` map. A value that is neither a
  known name nor a valid-format id returns a clear error (naming the valid
  lists) and makes **no** API call. A 24-char hex id that is not one of the
  configured lists is also rejected, so a wrong id can't silently target the
  wrong list.
- The title should be the task only. A trailing "(For Julia)" is stripped when
  `person` is a known name, so the target lives in `person`, not the title.

## Get Trello credentials

1. Go to <https://trello.com/app-key> and copy your **API key**.
2. Click **Token** → allow → copy the **OAuth token**.
3. In Trello, open the board. The board id is in the URL
   (`https://trello.com/b/<ID>/…`). Each list id is the long hex string in that
   list's URL. Configure them under `skills.credentials` as shown above — the
   `Backlog`/`Done` ids go in `TRELLO_LIST_ID`/`TRELLO_DONE_LIST_ID`, and all
   list + person names go in `TRELLO_LISTS`.

## Test

```bash
# From the skill directory (no live Trello needed — tests use httptest + in-process MCP):
CGO_ENABLED=0 go test ./...

# Cross-compile for your platform (famclaw ships CGO_ENABLED=0 binaries):
CGO_ENABLED=0 go build ./...
```

## Security notes

- The MCP server is a separate subprocess; famclaw launches stdio MCP servers
  under its landlock+seccomp sandbox by default. The trello-mcp binary only
  needs DNS + outbound HTTPS (to `api.trello.com`) and its environment, both of
  which the sandbox allows.
- `tools.web_fetch.url_allowlist` is unrelated to this skill — Trello API calls
  are made by the MCP server itself, not via famclaw's web tools.
- Without credentials the tools return a clear error; no request is sent to
  Trello, so misconfigured secrets cannot leak.
- List ids are never hardcoded in the skill; they come from config. Invalid
  list values are rejected before reaching the API.
