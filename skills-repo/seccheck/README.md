# seccheck

Security scanner for skills and MCP tool repositories.

## Installation

### FamClaw (built-in)

The `seccheck` command is built into the FamClaw binary:

```bash
famclaw seccheck https://github.com/someone/cool-skill
```

### Standalone binary

Download from [GitHub Releases](https://github.com/famclaw/skills/releases):

```bash
# Linux ARM64 (RPi 4/5)
curl -LO https://github.com/famclaw/skills/releases/latest/download/seccheck-linux-arm64
chmod +x seccheck-linux-arm64
sudo mv seccheck-linux-arm64 /usr/local/bin/seccheck

# Linux ARMv7 (RPi 3)
curl -LO https://github.com/famclaw/skills/releases/latest/download/seccheck-linux-armv7

# Linux AMD64
curl -LO https://github.com/famclaw/skills/releases/latest/download/seccheck-linux-amd64
```

### As a skill

```bash
# FamClaw
famclaw skill install famclaw/seccheck

# PicoClaw
picoclaw skills install famclaw/seccheck

# OpenClaw
clawhub install famclaw/seccheck
```

## Usage

```bash
seccheck <repo-url-or-path>
```

### Examples

```bash
# Scan a GitHub repo
seccheck https://github.com/someone/cool-skill

# Scan a local directory
seccheck ./my-skill/

# Scan and pipe JSON to jq
seccheck https://github.com/someone/tool 2>/dev/null | jq .verdict
```

## Output

Human-readable summary goes to stderr. JSON report goes to stdout.

```
SecCheck Report: cool-skill
Score: 85/100 — PASS

  [LOW]  Unused network import in utils.go
  [INFO] 3 dependencies, all clean
```

Exit code 0 = PASS/WARN, exit code 1 = FAIL.

## Building

From the famclaw repo root:

```bash
make build-seccheck
```

This builds static binaries for all three targets in `skills-repo/seccheck/bin/`.
