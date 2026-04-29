# Changelog

All notable changes to FamClaw are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/).

## Unreleased

### Added
- **Agent dispatch via `spawn_agent` builtin tool.** The parent LLM can
  now delegate sub-tasks to a different LLM profile by calling
  `spawn_agent(prompt, profile)`. The subagent runs on the specified
  profile (e.g., Qwen3-14B on local Ollama), has access to MCP tools,
  and returns its result to the parent conversation. Concurrency is
  controlled by the subagent scheduler (default: 2 concurrent).
- `LLMEndpointForProfile(name)` config helper for direct profile
  resolution by name (used by subagent executor).
- `BuiltinHandler` support in the agentcore tool loop — builtin tools
  (prefixed `builtin__`) route to a handler function instead of the
  MCP pool.

### Fixed
- **OPA policies are now embedded in the binary.** Previous releases
  required a clone of the source repo for the `policies/` directory —
  the binary alone would crash at startup with
  `policy: reading policy dir "./policies/family": no such file or directory`.
  Default policies now load from `go:embed` and ship inside the binary,
  so a downloaded release tarball runs standalone.

### Changed
- Production `.rego` files and `topics.json` moved from `policies/` (repo
  root) to `internal/policy/policies/`. OPA test commands now run against
  the new path (Makefile, CI workflow updated).
- Shipped configs (install scripts, build-image, e2e test, release smoke
  test, `config.yaml` example) no longer set `policies.dir` /
  `policies.data_dir`. Embedded is the default; the fields remain
  available in `config.yaml` for filesystem-based custom overrides.
- Startup log line distinguishes `Policies: embedded (built-in)` from
  `Policies: <path> (custom override)`.

### Upgrade notes
- If you have a `policies:` block in `/opt/famclaw/config.yaml` from
  an earlier install, remove it so the binary uses embedded policies:
  ```bash
  sudo sed -i '/^policies:/,/^$/d' /opt/famclaw/config.yaml
  ```
- The famclaw service warns at startup if `policies.dir` is set to a
  directory whose contents mirror the built-in policies — drop the
  block to silence the warning and use the embedded set.
