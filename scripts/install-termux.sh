#!/bin/bash

# Termux installation script for FamClaw
# This script installs FamClaw on Termux environment

set -e

echo "Installing FamClaw for Termux..."

# Check if we're running in Termux
if [ ! -d "/data/data/com.termux/files" ]; then
    echo "Error: This script must be run in Termux environment"
    exit 1
fi

# Install dependencies
pkg update
pkg install -y git go sqlite

# Clone the repository (if needed)
if [ ! -d "$HOME/famclaw" ]; then
    git clone https://github.com/famclaw/famclaw.git $HOME/famclaw
    cd $HOME/famclaw
else
    cd $HOME/famclaw
    git pull
fi

# Build FamClaw
go build -o famclaw cmd/famclaw/main.go

# Install the binary
mv famclaw /data/data/com.termux/files/usr/bin/famclaw

# Create config directory
mkdir -p $HOME/.config/famclaw

echo "FamClaw installed successfully for Termux!"
echo "Run 'famclaw --help' to get started."