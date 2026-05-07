# Advanced LLM Backends

## Meridian (multi-LLM routing and aggregation)

For teams that need dynamic routing across multiple LLM providers — e.g., falling back
from a local model to a cloud API, or load-balancing across endpoints — consider running
[Meridian](https://github.com/famclaw/meridian) as an OpenAI-compatible proxy in front
of FamClaw. Point `llm.base_url` at Meridian's local port and let it handle routing.
See the Meridian repo for setup instructions; FamClaw needs no special configuration
when Meridian is in place.

## claude_cli backend

If you already have [Claude Code](https://claude.ai/code) installed and want FamClaw to
route its LLM calls through the local `claude` binary, set `provider: claude_cli` in
your `config.yaml` under the `llm:` section. FamClaw will shell out to `claude -p
"<prompt>"` for each turn instead of making HTTP requests.

**Limitations in v1:** streaming and tool calls are not supported — the claude CLI
adapter calls `onToken` once with the full response and returns an error for
`ChatWithTools`. These limitations will be lifted in a future release.

## Recommended path for most users

The default Ollama path remains the recommended privacy-first choice. It keeps all
inference local, requires no API key, and works fully offline after the initial model
download. Use `claude_cli` or Meridian only if you have a specific reason to.
