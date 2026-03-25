#!/usr/bin/env bash
# firstboot-wizard.sh — Interactive first-boot setup wizard
# Prompts for family members and gateway configuration.
set -euo pipefail

CONFIG="/opt/famclaw/config.yaml"

echo ""
echo "═══ FamClaw Setup Wizard ═══"
echo ""

# ── Family members ────────────────────────────────────────────────────────────

echo "Let's set up your family members."
echo ""

USERS_YAML=""
USER_COUNT=0

# Always add a parent first
read -rp "Parent name (e.g., Mom): " PARENT_NAME
PARENT_NAME="${PARENT_NAME:-Mom}"
read -rsp "Parent PIN (4 digits): " PARENT_PIN
echo ""
PARENT_PIN="${PARENT_PIN:-1234}"

USERS_YAML="  - name: $(echo "$PARENT_NAME" | tr '[:upper:]' '[:lower:]')
    display_name: \"$PARENT_NAME\"
    role: parent
    pin: \"$PARENT_PIN\""

USER_COUNT=1

while true; do
    echo ""
    read -rp "Add a child? (y/n): " ADD_CHILD
    [ "$ADD_CHILD" = "y" ] || [ "$ADD_CHILD" = "Y" ] || break

    read -rp "  Child's name: " CHILD_NAME
    [ -n "$CHILD_NAME" ] || continue

    echo "  Age group:"
    echo "    1) Under 8"
    echo "    2) 8-12"
    echo "    3) 13-17"
    read -rp "  Choose (1-3): " AGE_CHOICE

    case "$AGE_CHOICE" in
        1) AGE_GROUP="under_8" ;;
        2) AGE_GROUP="age_8_12" ;;
        3) AGE_GROUP="age_13_17" ;;
        *) AGE_GROUP="under_8" ;;
    esac

    USERS_YAML="${USERS_YAML}
  - name: $(echo "$CHILD_NAME" | tr '[:upper:]' '[:lower:]')
    display_name: \"$CHILD_NAME\"
    role: child
    age_group: $AGE_GROUP"

    USER_COUNT=$((USER_COUNT + 1))
done

echo ""
echo "  ✓ $USER_COUNT family member(s) configured"

# ── Gateways ──────────────────────────────────────────────────────────────────

echo ""
echo "Which messaging gateways do you want to enable?"
echo ""

GW_YAML=""

read -rp "Enable Telegram bot? (y/n): " TG
if [ "$TG" = "y" ] || [ "$TG" = "Y" ]; then
    read -rp "  Telegram bot token: " TG_TOKEN
    GW_YAML="${GW_YAML}
  telegram:
    enabled: true
    token: \"$TG_TOKEN\""
    echo "  ✓ Telegram enabled"
fi

read -rp "Enable Discord bot? (y/n): " DC
if [ "$DC" = "y" ] || [ "$DC" = "Y" ]; then
    read -rp "  Discord bot token: " DC_TOKEN
    GW_YAML="${GW_YAML}
  discord:
    enabled: true
    token: \"$DC_TOKEN\""
    echo "  ✓ Discord enabled"
fi

# ── Write config ──────────────────────────────────────────────────────────────

# Update users section
if [ -n "$USERS_YAML" ]; then
    # Replace the users section in config.yaml
    python3 -c "
import yaml, sys
with open('$CONFIG') as f:
    cfg = yaml.safe_load(f)
# We'll just append — the full config management is via the web UI
" 2>/dev/null || true

    # Simple sed-based approach: replace users section
    sed -i '/^users:/,/^[a-z]/{ /^users:/d; /^  -/d; /^    /d; }' "$CONFIG" 2>/dev/null || true
    echo -e "users:\n$USERS_YAML" >> "$CONFIG"
fi

if [ -n "$GW_YAML" ]; then
    echo -e "gateways:$GW_YAML" >> "$CONFIG"
fi

echo ""
echo "  ✓ Configuration saved to $CONFIG"
echo "  Edit anytime: nano $CONFIG"
echo "  Then restart: sudo systemctl restart famclaw"
