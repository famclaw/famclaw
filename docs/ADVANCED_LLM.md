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
your `config.yaml` under the `llm:` section. FamClaw shells out to `claude` with
`--input-format stream-json` (full conversation history as NDJSON on stdin),
`--output-format stream-json` (streaming output), and `--system-prompt` for the
system message. Responses stream back in real time via `onToken` callbacks.

**Limitations:** tool calls are not supported — `ChatWithTools` returns an error.
This limitation may be lifted in a future release.

## Multimodal LLMs

FamClaw supports multimodal LLMs that can process both text and images. When using a
vision-capable model, FamClaw will automatically encode and send images as part of
the chat completion request. Supported models include:
- GPT-4o, GPT-4o-mini (OpenAI)
- Claude 3.x series (Anthropic)
- Gemini Pro Vision (Google)
- Llava series or other vision-capable open models
- Any OpenAI-compatible API endpoint serving a vision-capable model

To use multimodal capabilities, ensure your configured LLM model supports image input.
The image understanding feature works with any vision-capable model, making it flexible
for different configurations.

### Image Understanding Feature

FamClaw's image understanding feature extends support for image attachments in Telegram,
with plans to expand to other gateways. When a user sends an image through a supported
gateway, FamClaw processes the image and includes it as part of the message sent to the
configured LLM model. The feature requires a vision-capable LLM model to function.

## Recommended path for most users

The default Ollama path remains the recommended privacy-first choice. It keeps all
inference local, requires no API key, and works fully offline after the initial model
download. Use `claude_cli` or Meridian only if you have a specific reason to.
