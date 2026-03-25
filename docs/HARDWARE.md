# Hardware Guide

FamClaw runs on Raspberry Pi and Mac Mini. This guide helps you choose the right hardware.

## Recommended hardware

| Hardware | RAM | Best for | LLM Model | Response time |
|----------|-----|----------|-----------|---------------|
| **RPi 5 (8GB)** | 8GB | Best Pi experience | `llama3.1:8b` | ~3-5s |
| **RPi 5 (4GB)** | 4GB | Great for most families | `llama3.2:3b` | ~2-4s |
| **RPi 4 (4GB)** | 4GB | Good, widely available | `llama3.2:3b` | ~4-6s |
| **RPi 4 (2GB)** | 2GB | Budget option | `phi3:mini` | ~5-8s |
| **RPi 3B+** | 1GB | Minimal, slow | `tinyllama` | ~8-15s |
| **Mac Mini M1+** | 8-16GB | Fastest home option | `llama3.1:8b` | ~1-2s |

## What you need

### Minimum
- Raspberry Pi 3B+ or newer
- 16GB microSD card (Class 10 / A1)
- 5V power supply (USB-C for Pi 4/5, micro-USB for Pi 3)
- Network connection (ethernet recommended for setup)

### Recommended
- Raspberry Pi 4/5 with 4GB+ RAM
- 32GB microSD card (A2 rated)
- Official Raspberry Pi power supply
- Ethernet cable (faster than WiFi for LLM downloads)
- Case with passive cooling (Pi 5 runs warm under load)

---

## Storage

| SD Card | Speed | Recommendation |
|---------|-------|---------------|
| 16GB Class 10 | Minimum | Tight — tinyllama only |
| 32GB A1 | Good | Most models fit |
| 64GB A2 | Best | Room for multiple models |

LLM model sizes:
- `tinyllama`: ~600MB
- `phi3:mini`: ~2GB
- `llama3.2:3b`: ~2GB
- `llama3.1:8b`: ~5GB

---

## Power

| Pi Model | Power Supply | Notes |
|----------|-------------|-------|
| Pi 3B+ | 5V 2.5A micro-USB | |
| Pi 4 | 5V 3A USB-C | Official PSU recommended |
| Pi 5 | 5V 5A USB-C | **Must use Pi 5 PSU** — 3A not enough under load |

Undervoltage causes random crashes during LLM inference. Use the official power supply.

---

## Cooling

LLM inference is CPU/GPU intensive. Without cooling:
- Pi 4: throttles after ~2 min continuous use
- Pi 5: throttles after ~1 min

Recommendations:
- **Pi 4:** Aluminum heatsink case (passive) — sufficient
- **Pi 5:** Active cooler (official fan) or heatsink case with fan

---

## Network

- **First boot:** Downloads Ollama + LLM model (600MB–5GB). Use ethernet.
- **After setup:** FamClaw's policy engine and web UI work offline. LLM calls require network access to the configured endpoint (local LAN or cloud).
- **mDNS:** FamClaw advertises as `famclaw.local` on your LAN.

---

## Old Android phones (Termux)

FamClaw can run on old Android phones via Termux. See [ANDROID.md](./ANDROID.md).

**Important:** The LLM runs on a separate device (another Pi, Mac, or cloud). The Android phone is just the FamClaw gateway + policy engine.

---

## Mac Mini

For the best home experience, a Mac Mini M1+ runs everything locally with fast inference:

```bash
# Install Ollama
brew install ollama
ollama pull llama3.1:8b

# Install FamClaw
brew install famclaw/tap/famclaw
# Or download from GitHub Releases

famclaw --config config.yaml
```
