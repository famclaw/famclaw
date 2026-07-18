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
- `storage.db_path` - Database file path
- `gateways.telegram.token` - Telegram bot token (from @BotFather)
- `gateways.discord.token` - Discord bot token (from Discord Developer Portal)

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
