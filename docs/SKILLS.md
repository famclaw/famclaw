# Skills

Skills extend FamClaw with new tools and capabilities. They're shared across FamClaw, PicoClaw, and OpenClaw.

## Installing a skill

> **Note:** The CLI skill management commands are planned. Currently, skills are installed manually by placing the SKILL.md in the skills directory.

### Manual installation

```bash
# Download the skill
git clone https://github.com/famclaw/skills /tmp/famclaw-skills

# Copy to skills directory
cp -r /tmp/famclaw-skills/seccheck ~/.famclaw/skills/seccheck

# Restart FamClaw
sudo systemctl restart famclaw
```

### Planned CLI (not yet implemented)

```bash
famclaw skill install famclaw/seccheck
famclaw skill list
famclaw skill remove seccheck
famclaw skill disable seccheck
famclaw skill enable seccheck
```

---

## How skills work

Each skill is defined by a `SKILL.md` file with YAML frontmatter:

```markdown
---
name: seccheck
description: Scan repos for security issues
version: "1.0"
author: famclaw
tags: [security]
platforms: [linux, darwin]
requires:
  bins: [seccheck]
---
# Instructions for the AI

When the user asks to check a skill, run `seccheck <url>`.
```

The **frontmatter** declares metadata. The **body** is injected into the AI's system prompt as AgentSkills XML, telling the AI when and how to use the skill.

### Skill name validation

The `name` field becomes the skill's on-disk directory. It is validated at install time and must match `^[a-zA-Z0-9_-]{1,64}$` — alphanumerics (any case), hyphens, and underscores only, 1–64 characters. Names containing path separators (`/`, `\`), `..`, or control characters are rejected.

### Environment allowlist

By default, FamClaw passes only four variables to every skill subprocess: `HOME`, `LANG`, `PATH`, `TZ`. All other process environment variables are stripped.

If a skill needs additional credentials (API keys, tokens, etc.), it can declare them in its frontmatter:

```yaml
---
name: my-skill
description: What it does
env_allowlist:
  - MY_API_KEY
  - CUSTOM_CONFIG_PATH
---
```

FamClaw will then pass through any of those keys present in its process environment. The allowlist is **case-sensitive** and additive — the default four variables are always included regardless of frontmatter.

A hard blocklist protects credentials that must never reach a skill subprocess, regardless of frontmatter: bot tokens, LLM API keys, HMAC secrets, SMTP passwords, and other sensitive variables are always rejected even if declared in `env_allowlist`.

---

## Writing a skill

### 1. Create SKILL.md

```markdown
---
name: my-skill
description: What it does
version: "0.1"
author: your-name
tags: [tag1, tag2]
platforms: [linux, darwin]
requires:
  bins: [my-tool]
---
# My Skill

When the user asks about X, run `my-tool <args>`.
Report the output back to the user.
```

### 2. Create the tool binary

Skills can use any binary. Build it as a static binary (no CGO):

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o my-tool ./cmd/my-tool
```

### 3. Test locally

```bash
# Copy SKILL.md to the skills directory
cp -r my-skill/ ~/.famclaw/skills/my-skill/

# Restart FamClaw
sudo systemctl restart famclaw
```

### 4. Submit to the registry

1. Fork [famclaw/skills](https://github.com/famclaw/skills)
2. Add your skill directory with `SKILL.md`
3. Open a PR

---

## Security scanning

Before installing any third-party skill, FamClaw runs `seccheck`:

```bash
famclaw seccheck https://github.com/someone/cool-skill
```

This checks for:
- Hardcoded secrets
- Suspicious network calls
- Known CVEs in dependencies
- Typosquatting
- Runs the tool in a sandbox

Skills that fail the security check are not installed.

---

## MCP integration

Skills can also be MCP (Model Context Protocol) tool servers. FamClaw connects to them and the AI can call tools during conversations:

1. User asks a question
2. AI decides to use a tool
3. FamClaw calls the MCP server (local or remote)
4. Tool result is fed back to the AI
5. AI responds with the final answer

Maximum 10 tool call iterations per conversation turn.

### MCP transport types

Configure MCP servers in `config.yaml` under `skills.mcp_servers`:

**Stdio (local process)** — for devices that can run tool binaries (Mac, beefy RPi):

```yaml
skills:
  mcp_servers:
    seccheck:
      transport: stdio
      command: seccheck
      args: ["--json"]
```

**HTTP (remote server)** — for constrained devices (Android, RPi-as-gateway) connecting to tools on LAN:

```yaml
skills:
  mcp_servers:
    remote-tools:
      transport: http
      url: "http://192.168.1.10:3001/mcp"
      headers:
        Authorization: "Bearer ${MCP_TOKEN}"
```

**SSE (legacy)** — for older MCP servers using Server-Sent Events:

```yaml
skills:
  mcp_servers:
    legacy:
      transport: sse
      url: "http://192.168.1.10:3002/sse"
```

Servers are enabled by default. Add `disabled: true` to skip without removing.

### MCP server management

You can list, add, and remove MCP servers at runtime — no `config.yaml` edit required. Changes are persisted to `config.yaml` and take effect immediately (the MCP pool is reloaded in place; see *Configuration hot-reload* in `docs/AGENT_SETUP.md`).

**Web API** (session-authenticated, parent role required):

| Method | Endpoint | Body |
|---|---|---|
| `GET`    | `/api/mcp`        | — lists configured servers |
| `POST`   | `/api/mcp/add`    | `{"name": "<name>", "config": { ...MCPServerConfig... }}`; rejects duplicate names with `400` |
| `POST`   | `/api/mcp/remove` | `{"name": "<name>"}` |

These are also surfaced on the dashboard **Skills** card.

**Chat commands** (parent-gated, from any linked gateway):

```
.mcp list                          # list configured servers
.mcp add <name> <transport> <k=v>  # add a server
.mcp remove <name>                 # remove a server
```

`<transport>` is `stdio`, `http`, or `sse`. Supported keys: `command`, `args` (comma-separated), `url`, `headers` (comma-separated `Key=Value`), `disabled` (`true`/`false`).
