---
name: seccheck
description: Scan a skill or MCP git repository for security issues before installing. Checks for secrets, malicious network calls, CVEs, typosquatting, and runs in a sandbox.
version: "1.0"
author: famclaw
tags: [security, skills, mcp]
platforms: [linux, darwin]
requires:
  bins: [seccheck]
---
# SecCheck

Security scanner for skills and MCP tool repositories. Run it before installing any third-party skill.

## What it checks

| Check | Description |
|-------|-------------|
| **Secrets** | Hardcoded API keys, tokens, passwords, private keys |
| **Network** | Suspicious outbound HTTP/DNS calls, data exfiltration patterns |
| **Supply chain** | Typosquatting in dependencies, suspicious package names |
| **CVEs** | Known vulnerabilities via osv.dev |
| **Sandbox** | Runs the tool in a restricted sandbox (Docker/sandbox-exec) |

## Usage

When the user asks to check a skill or scan a repository:

```bash
seccheck <repo-url-or-path>
```

### Examples

- "Is this skill safe to install?" → run seccheck on the repo
- "Check this MCP server before I add it" → run seccheck
- "Scan https://github.com/someone/cool-skill" → `seccheck https://github.com/someone/cool-skill`

## Output

SecCheck produces a score (0-100), a verdict (PASS/WARN/FAIL), and a detailed report:

```
SecCheck Report: cool-skill
Score: 85/100 — PASS

Findings:
  [LOW]  Unused network import in utils.go
  [INFO] 3 dependencies, all clean

Recommendation: Safe to install.
```

If the verdict is FAIL, warn the user and explain the findings. Do not install the skill.

## Compatibility

| Runtime | How to install |
|---------|---------------|
| FamClaw | `famclaw skill install famclaw/seccheck` |
| PicoClaw | `picoclaw skills install famclaw/seccheck` |
| OpenClaw | `clawhub install famclaw/seccheck` |
