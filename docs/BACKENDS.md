# Supported LLM Backends

FamClaw works with any OpenAI-compatible API. No code changes needed — just set `base_url` in config.

## Inference Engines

| Engine | Platform | Best for | Tool Calling |
|--------|----------|----------|-------------|
| **Ollama** | All | Easiest setup, MLX on Mac since v0.19 | Yes |
| **llama.cpp** | All | RPi (10-20% faster than Ollama) | Yes + MCP |
| **vllm-mlx** | Mac only | Fastest on Apple Silicon (400+ tok/s) | Yes |
| **LocalAI** | All | Multi-backend, production | Yes |
| **LM Studio** | Desktop | GUI + API, developer-friendly | Yes |
| **llamafile** | All | Single-file, zero install | Yes |
| **vLLM** | NVIDIA/AMD | Production GPU serving | Yes |

## Recommended by Hardware

| Hardware | Engine | Model | Config |
|----------|--------|-------|--------|
| Mac Mini M4+ 16GB | Ollama 0.19+ | qwen3:4b | `base_url: http://localhost:11434` |
| Mac Mini M1 16GB | Ollama or vllm-mlx | qwen3:4b | `base_url: http://localhost:11434` |
| RPi 5 8GB | Ollama or llama.cpp | qwen3:1.7b | `base_url: http://localhost:11434` |
| RPi 4 4GB | Ollama | qwen3:1.7b | `base_url: http://localhost:11434` |
| RPi 3 1GB | N/A (use remote) | — | Point to LAN or cloud |
| NVIDIA GPU | vLLM | gemma4:31b | `base_url: http://localhost:8000/v1` |
| Cloud only | — | Any | `base_url: https://api.openai.com/v1` |

## Recommended Models

All recommended models are Apache-2.0 and pass real on-device tool-call tests (3/3).
Sizes are Q4_K_M. See the README for full benchmark methodology.

| Model | Params | RAM (Q4) | Tool Calling | License | Best for |
|-------|--------|----------|-------------|---------|----------|
| **qwen3:1.7b** | 2.0B | ~2 GB | 3/3 real | Apache 2.0 | Pi 5 (8 GB) / ≤8 GB default |
| **qwen3:4b** | 4.0B | ~3.5 GB | 3/3 real | Apache 2.0 | 16 GB+ machines |
| **gemma4:31b** | 31B | ~20 GB | 3/3 real | Apache 2.0 | 64 GB Mac / capable box |
| **deepseek-r1:7b** | 7B | ~5GB | Yes | MIT | Best reasoning |
| tinyllama | 1.1B | ~600MB | Limited | Apache 2.0 | Last resort |

## Cloud Providers

Any OpenAI-compatible API works:

```yaml
llm:
  profiles:
    openai:
      base_url: "https://api.openai.com/v1"
      model: "gpt-4o-mini"
      api_key: "${OPENAI_API_KEY}"
    anthropic:
      base_url: "https://api.anthropic.com/v1"
      model: "claude-sonnet-4-20250514"
      api_key: "${ANTHROPIC_API_KEY}"
    openrouter:
      base_url: "https://openrouter.ai/api/v1"
      model: "google/gemma-4-e4b"
      api_key: "${OPENROUTER_API_KEY}"
```

## Setup

```bash
# Ollama (recommended)
ollama pull qwen3:1.7b
./famclaw  # wizard detects hardware, recommends model

# llama.cpp (RPi performance)
llama-server -m gemma-4-e2b-Q4_K_M.gguf --port 11434

# vLLM (GPU)
vllm serve google/gemma-4-E4B-it --port 8000
```
