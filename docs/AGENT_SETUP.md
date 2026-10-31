# Agent Setup Guide for FamClaw

This document provides a step-by-step guide for coding agents (Claude Code, Opencode, Pi, Goose, Codex, etc.) to install and configure FamClaw.

## Prerequisites

1. **Go Version**: FamClaw requires Go 1.26.0 or higher (see `go.mod`)
2. **Platform Support**: Linux, macOS, and Raspberry Pi (ARM64/ARMv7) 
3. **Dependencies**: Git, make, and a working Go toolchain

## Build and Installation

### Clone the Repository
```bash
git clone https://github.com/famclaw/famclaw
cd famclaw
```

### Build the Binary
```bash
make build
```

The binary will be created at `bin/famclaw`.

### Install as a Service (Optional)
```bash
make install-service
```

This installs FamClaw as a systemd service on Linux or launchd on macOS.

## Minimal Configuration

Copy the default config file and set essential values:
```bash
cp config.yaml config.local.yaml
```

Edit `config.local.yaml` to set:
1. **LLM Endpoint**: Base URL and model for your LLM (e.g., Ollama, OpenAI, Anthropic)
2. **Storage Path**: Database location
3. **Gateway Tokens**: At least one messaging gateway token (Telegram, Discord, or Web)

### Example Minimal Configuration
```yaml
server:
  host: "0.0.0.0"
  port: 8080

llm:
  base_url: "http://localhost:11434"  # Ollama on local machine
  model: "llama3.2:3b"
  # vision_profile: "vision"  # vision-capable model for image messages (see ADVANCED_LLM)

storage:
  db_path: "./data/famclaw.db"

users:
  - name: "parent"
    display_name: "Parent"
    role: "parent"
    pin: "${PARENT_PIN}"
    color: "#6366f1"

gateways:
  telegram:
    enabled: true
    token: "${TELEGRAM_BOT_TOKEN}"
```

## Running and Verification

### Start FamClaw
```bash
./bin/famclaw --config config.local.yaml
```

The `--config` flag is the only lookup (default `config.yaml` in the current directory). `make install-service` runs with `--config` pointing at `/opt/famclaw/config.yaml`.

### Verify Setup
1. FamClaw should bind to `:8080` and return HTTP 307 redirects to `/setup` before initial configuration is complete
2. Access `http://localhost:8080` in your browser to begin the setup wizard
3. Complete the setup wizard to configure your family profiles and messaging gateways

## Common Gotchas

1. **Environment Variables**: Set required environment variables like `PARENT_PIN` and gateway tokens
2. **Directory Permissions**: Ensure `sandbox_root` (if configured) is a valid directory
3. **Service Installation**: On macOS, `install-service` uses launchd; on Linux, it uses systemd
4. **MCP Initialization**: The MCP tool initialization is non-fatal and can be skipped
5. **Database Path**: Ensure `db_path` directory exists and is writable
6. **Firewall Settings**: Make sure port 8080 is accessible if running remotely

## Build Targets

The following make targets are available:
- `make build` - Build for current machine  
- `make install` - Install binary to `/usr/local/bin`
- `make install-service` - Install as systemd (Linux) or launchd (macOS) service
- `make cross` - Build for all supported platforms
- `make cross-rpi3` - Build for Raspberry Pi 2/3/Zero (ARMv7)
- `make cross-rpi4` - Build for Raspberry Pi 4/5 (ARM64)
- `make cross-mac-intel` - Build for Mac Intel
- `make cross-mac-arm` - Build for Mac Apple Silicon
- `make cross-linux64` - Build for generic Linux x86_64

## Configuration Keys

Essential configuration keys to set:
- `llm.base_url` - LLM endpoint URL (e.g., `http://localhost:11434`)
- `llm.model` - LLM model name (e.g., `llama3.2:3b`)
- `llm.vision_profile` - vision-capable model profile used for image messages (leave empty if your main model is vision-capable; see ADVANCED_LLM)
- `storage.db_path` - Database file path
- `gateways.telegram.token` - Telegram bot token (from @BotFather)
- `gateways.discord.token` - Discord bot token (from Discord Developer Portal)
- `tools.web_search.enabled` - enable web search (the SearXNG provider requires `tools.web_fetch.enabled=true`; the hosted `tavily` provider does not)
- `tools.web_search.provider` - `searxng` (default) or `tavily` (hosted search API); an unknown value is a config error listing the supported ones
- `tools.web_search.endpoint` - SearXNG JSON search URL, e.g. `http://localhost:8888` (SearXNG only; required when `web_search` is enabled with the default provider). The endpoint host must be in `tools.web_fetch.url_allowlist`
- `FAMCLAW_WEB_SEARCH_API_KEY` - env var for the hosted provider (Tavily) API key; preferred over `tools.web_search.tavily_api_key` in YAML (env wins; a YAML-set key logs a warning). Note: a hosted provider sends the user's query text to the vendor's API
- `tools.web_fetch.block_private_networks` - opt-in: block loopback/RFC1918/ULA at the dialer (default false)

## HoneyBadger Security Scanning (Optional but Recommended)

FamClaw includes an optional security scanning feature powered by the external `honeybadger` binary. This tool scans skills and tools for safety upon installation and via asynchronous background runtime scans, and can quarantine tools that fail the scan.

**What it is:** The HoneyBadger scanner shells out to an external `honeybadger` binary (see `internal/honeybadger/client.go`). It runs on skill install (if `seccheck.auto_seccheck` is true) and periodically in the background (if `seccheck.runtime_scan` is true). Tools that fail the scan can be quarantined (blocked from use) based on configuration.

**Requirement:** The `honeybadger` binary must be installed and available in your system's `PATH`. The `Available()` function uses `exec.LookPath("honeybadger")` to check. If the binary is not found, scanning is disabled and a warning is logged.

**Config:** HoneyBadger scanning is configured under the `seccheck:` section in `config.yaml` (labeled "Security Scanning (HoneyBadger)" in the example config). The real keys are:
- `enabled`: Master switch — when false, no scanning anywhere.
- `auto_seccheck`: Scan skills before writing to disk (install-time).
- `block_on_fail`: Refuse to install a skill if the scan fails (FAIL verdict).
- `paranoia`: Scanning level — one of `minimal`, `family`, `strict`, or `paranoid`.
- `runtime_scan`: Enable asynchronous background scans of installed tools.
- `rescan_interval`: How often to re-scan tools (e.g., "168h" for weekly).
- `async_scan_timeout`: Timeout per background scan (e.g., "60s").
- `quarantine_on_fail`: Block tools from use after a FAIL verdict.
- `notify_on_quarantine`: Send a parent notification when a tool is quarantined.

> **Note:** The internal `SecCheckConfig` struct in `internal/config/config.go` is marked as deprecated in favor of a future `honeybadger` key, but the current configuration remains under `seccheck:`.

This feature is **optional but recommended** for a family assistant to help ensure that skills and tools do not contain malicious content.

## Optional Features

- **Policy/OPA rules:** Enable custom policies by setting `policies.dir` and `policies.data_dir` in `config.yaml` to point to directories containing `.rego` and JSON data files.
- **Parent notifications:** Configure alert channels under `notifications:` in `config.yaml` (email, Slack, Discord, SMS, ntfy) to receive alerts about quarantined tools, approval requests, etc.
- **MCP servers:** Enable multi-tool servers by configuring `skills.mcp_servers` in `config.yaml` (see the example in the default config).
- **Web tools:** The built-in web tools (`web_fetch`, `web_search`, `browser`) can be enabled under `tools:` in `config.yaml`. `web_fetch` extracts HTML→text, plain/JSON, and PDF text layers; hosts are gated by a URL policy (in `allowlist` mode a non-empty `url_allowlist` is required — an empty list denies all, the legacy SSRF guard — while in the default `denylist` mode public hosts are allowed and only `url_denylist` entries are blocked; see "URL Policy Configuration" below). Its optional headless-browser fallback (`fallback_to_browser`) fires for thin JS-heavy pages and on 403/429 bot-blocks, and requires `tools.browser.enabled` plus a reachable browser pool. `web_search` queries SearXNG (default — requires `tools.web_fetch.enabled=true` and reuses its `url_allowlist` as the host gate) or hosted Tavily (`provider: tavily` + the `FAMCLAW_WEB_SEARCH_API_KEY` env var). See the README "Web fetch" / "Web search" sections.

### URL Policy Configuration

The `tools.web_fetch` section supports two URL policy modes:

1. **Allowlist mode** (`url_policy: allowlist`): Only hosts in `url_allowlist` are permitted. 
   - Requires at least one host in `url_allowlist`
   - Empty list denies all (SSRF guard)
   - Hosts are matched exactly or as subdomains (e.g., "example.com" matches "www.example.com")

2. **Denylist mode** (`url_policy: denylist`): All public hosts are allowed by default, with explicit blocks in `url_denylist`.
   - Includes a bundled kid-safe baseline (Aegis Blocklist) that blocks adult content, hate groups, and harmful sites
   - Parent-maintained denylist entries in `url_denylist` (exact or subdomain matches)
   - Hosts are matched exactly or as subdomains
   - Suspicious site detector (W4) that can block sites based on reputation

The default mode is `denylist` when `url_allowlist` is empty, providing a secure-by-default experience.

**Configuration example:**
```yaml
tools:
  web_fetch:
    enabled: true
    url_policy: "denylist"
    url_denylist:
      - "adult.example.com"
      - "hate.example.com"
    max_bytes: 262144
    timeout_seconds: 30
    allowed_roles:
      - "parent"
      - "child"
  web_search:
    enabled: true
    provider: "searxng"
    endpoint: "http://localhost:8888"
```

The `url_policy` field accepts "allowlist" or "denylist" values. When unset, the mode is determined by the presence of an `url_allowlist`:
- Non-empty allowlist → `allowlist` mode (backwards compatible)
- Empty/missing allowlist → `denylist` mode (secure-by-default)

### Admin Tools

Parent admins can manage web fetch settings via:
- Allowlist: managed conversationally via the LLM admin tools `builtin__allowlist_list` / `builtin__allowlist_add` / `builtin__allowlist_remove` (internal/agent/tools/admin/).
- Denylist: managed conversationally via the LLM admin tools `builtin__denylist_list` / `builtin__denylist_add` / `builtin__denylist_remove` (internal/agent/tools/admin/).
- Both lists persist in config.yaml under `tools.web_fetch.url_allowlist` / `url_denylist` and can be read/updated via the `/api/settings` GET/POST endpoint.

These endpoints are only available to users with the "parent" role.
- **MCP runtime management:** List and add MCP servers conversationally via the parent-only `mcp_list` and `mcp_add` built-in tools, or add/remove via the `/api/mcp` endpoints (session-authenticated, parent role) — no config edit or restart required. The running MCP pool is reloaded in place. See [SKILLS.md](./SKILLS.md) and the MCP section of the README.
- **Config hot-reload:** FamClaw watches `config.yaml` and reloads non-destructive changes in place — including MCP server add/remove, gateway toggles, and tool config — without a restart. Each component reports its actual reload outcome (success, failure, or "requires restart" for non-reloadable components); false success is no longer reported. The voice transcriber is hot-reloadable. The file watcher stops cleanly on shutdown.
## Next Steps

After successful setup, FamClaw will:
1. Listen on `:8080` 
2. Serve the web UI at `http://localhost:8080`
3. Connect to configured messaging gateways
4. Process messages through the policy engine

For advanced configuration, see `docs/ADVANCED_LLM.md` and `docs/GATEWAYS.md`.

---

This setup guide is designed for coding agents to provision FamClaw autonomously. 
It covers the minimum required steps for deployment and configuration.

For more information about FamClaw's architecture and features, see:
- [Main README](../README.md)
