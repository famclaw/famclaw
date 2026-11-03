# FamClaw on Raspberry Pi - Local Installation Guide

This document provides a comprehensive guide for installing FamClaw on a Raspberry Pi, covering hardware requirements, installation steps, and configuration for optimal performance.

> **Path conventions.** Paths in the examples use `/home/user` as a generic
> placeholder home directory. Substitute your actual home directory (e.g. the
> default `pi` account on Raspberry Pi OS, or your own user home on Linux) before
> running the commands.

## Hardware Requirements

### Recommended Hardware
| Hardware | RAM | LLM Model | Notes |
|----------|-----|-----------|-------|
| **RPi 5 (8GB)** | 8GB | `qwen3:1.7b` | Best Pi experience, 3/3 tool calls |
| **RPi 5/4 (4GB)** | 4GB | `qwen3:1.7b` | Fits comfortably, Apache 2.0 |

### Minimum Requirements
- Raspberry Pi 3B+ or newer
- 16GB microSD card (Class 10 / A1)
- 5V power supply (USB-C for Pi 4/5, micro-USB for Pi 3)
- Network connection (ethernet recommended for setup)

### Storage Recommendations
| SD Card | Speed | Recommendation |
|---------|-------|----------------|
| 16GB Class 10 | Minimum | Tight — tinyllama only |
| 32GB A1 | Good | Most models fit |
| 64GB A2 | Best | Room for multiple models |

LLM model sizes:
- `tinyllama`: ~600MB
- `phi3:mini`: ~2GB
- `llama3.2:3b`: ~2GB
- `llama3.1:8b`: ~5GB

## Installation Steps

### 1. Prepare the SD Card
1. Download Raspberry Pi OS Lite (64-bit) from https://www.raspberrypi.com/software/
2. Flash the OS to your microSD card using Raspberry Pi Imager or balenaEtcher
3. Enable SSH by creating an empty `ssh` file in the boot partition

### 2. Initial Setup
1. Insert the SD card into your Pi and power it on
2. Connect via SSH: `ssh pi@<pi-ip-address>`
3. Change the default password: `sudo passwd pi`

### 3. Install FamClaw
#### Option A: Using Homebrew (Recommended)
```bash
# Install Ollama
sudo apt-get update
sudo apt-get install -y curl
curl -fsSL https://ollama.com/install.sh | sh

# Install FamClaw
brew install famclaw/tap/famclaw
```

#### Option B: Manual Installation
```bash
# Install Ollama
curl -fsSL https://ollama.com/install.sh | sh

# Download FamClaw binary
wget https://github.com/famclaw/famclaw/releases/latest/download/famclaw-linux-arm64 -O famclaw
chmod +x famclaw

# Pull recommended model
ollama pull qwen3:1.7b
```

### 4. Configure FamClaw
Create a configuration file at `~/.config/famclaw/config.yaml`:
```yaml
llm:
  profiles:
    default:
      base_url: "http://localhost:11434"
      model: "qwen3:1.7b"
      api_key: "your-api-key" # local Ollama: key is unused

# Optional: Set up a specific directory for data
data_dir: "/home/user/famclaw-data"
```

### 5. Start FamClaw
```bash
# Start the service
sudo systemctl enable famclaw
sudo systemctl start famclaw

# Or run manually
./famclaw --config ~/.config/famclaw/config.yaml
```

## Performance Optimization

### System Configuration
1. Increase swap space if needed:
```bash
sudo dphys-swapfile swapoff
sudo nano /etc/dphys-swapfile
# Change CONF_SWAPSIZE=100 to CONF_SWAPSIZE=200
sudo dphys-swapfile setup
sudo dphys-swapfile swapon
```

2. Optimize Ollama settings:
```bash
# Edit /etc/systemd/system/ollama.service
# Add to [Service] section:
Environment=OLLAMA_MODELS=/home/user/ollama-models
```

### Model Selection
The recommended model for RPi 5 is `qwen3:1.7b` as it:
- Fits within 4GB RAM
- Uses Apache 2.0 license
- Passes real on-device tool-call tests (3/3)
- Runs efficiently on ARM architecture

## Troubleshooting

### Common Issues
1. **Low RAM**: If you encounter out-of-memory errors, reduce model size or add swap space
2. **Network Connectivity**: Ensure proper internet connection for initial model downloads
3. **Permission Errors**: Make sure all directories have correct ownership

### Verification
1. Check if FamClaw is running:
```bash
ps aux | grep famclaw
```

2. Verify Ollama is accessible:
```bash
ollama list
```

3. Access the web UI at `http://<pi-ip>:8080`

## Maintenance

### Updates
Regularly update FamClaw:
```bash
brew update
brew upgrade famclaw
```

### Backup Strategy
1. Regular backups of the data directory:
```bash
tar -czf famclaw-backup-$(date +%Y%m%d).tar.gz /home/user/famclaw-data
```

2. Export configuration:
```bash
cp ~/.config/famclaw/config.yaml ./famclaw-config-backup.yaml
```

## Support Resources

- Documentation: https://github.com/famclaw/famclaw/tree/main/docs
- Community Support: https://github.com/famclaw/famclaw/discussions
- Issue Tracker: https://github.com/famclaw/famclaw/issues