#!/bin/sh
# envcheck.sh — exits 0 if a blocked env var was leaked, 1 otherwise.
# Called by TestEnvIsolation to verify skill subprocesses cannot read secrets.
FOUND=0
for var in FAMCLAW_LLM_API_KEY TELEGRAM_TOKEN DISCORD_TOKEN WHATSAPP_TOKEN HMAC_SECRET; do
  if [ -n "${!var:-}" ]; then
    echo "LEAKED: $var" >&2
    FOUND=1
  fi
done
exit $FOUND
