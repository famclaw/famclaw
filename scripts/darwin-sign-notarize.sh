#!/usr/bin/env bash
# FamClaw darwin code-signing + notarization (release gate).
#
# Invoked from the goreleaser builds.hooks.post entry in .goreleaser.yaml for
# every darwin binary, and as `--check` by the release workflow preflight
# step.
#
# Why this gate exists (issues #311, #214):
#   - an unsigned / ad-hoc signed arm64 Mach-O binary is killed by the kernel
#     on launch: exit 137, no output — indistinguishable from a corrupt
#     binary to the user;
#   - a binary that is not signed with a Developer ID Application
#     certificate and notarized is blocked by Gatekeeper after a browser
#     download (com.apple.quarantine), so non-technical users cannot
#     install it at all.
#
# The release therefore REFUSES to ship a darwin binary unless it can sign,
# notarize, and staple it. Missing credentials fail the release LOUD with an
# actionable message — there is no unsigned fallback.
#
# Credentials are injected at release time as environment variables (GitHub
# repository secrets with the same names; see docs/RELEASE.md). Never commit
# credential material to this repo.
#
#   FAMCLAW_APPLE_P12            base64 .p12 containing the "Developer ID
#                                Application" certificate + private key
#   FAMCLAW_APPLE_PASSPHRASE     passphrase the .p12 was exported with
#   FAMCLAW_APPLE_TEAM_ID        Apple developer team ID
#   Notarization auth — either an App-Services notary key (preferred):
#   FAMCLAW_NOTARY_KEY           base64 notarytool API key .p12
#   FAMCLAW_NOTARY_KEY_ID        its key ID
#   FAMCLAW_NOTARY_ISSUER_ID     its App Services issuer ID
#   or Apple ID + app-specific password:
#   FAMCLAW_APPLE_ID             Apple ID
#   FAMCLAW_APPLE_PASSWORD       app-specific password
#
# Usage:
#   darwin-sign-notarize.sh <darwin-binary>   sign + notarize + staple + verify
#   darwin-sign-notarize.sh --check           exit 0 iff credentials are complete
set -euo pipefail

die() { echo "ERROR: $*" >&2; exit 1; }

check_credentials() {
  local missing=()
  [ -n "${FAMCLAW_APPLE_P12:-}" ] || missing+=("FAMCLAW_APPLE_P12")
  [ -n "${FAMCLAW_APPLE_PASSPHRASE:-}" ] || missing+=("FAMCLAW_APPLE_PASSPHRASE")
  [ -n "${FAMCLAW_APPLE_TEAM_ID:-}" ] || missing+=("FAMCLAW_APPLE_TEAM_ID")
  if [ -z "${FAMCLAW_NOTARY_KEY:-}${FAMCLAW_APPLE_ID:-}" ]; then
    missing+=("FAMCLAW_NOTARY_KEY + FAMCLAW_NOTARY_KEY_ID + FAMCLAW_NOTARY_ISSUER_ID (or FAMCLAW_APPLE_ID + FAMCLAW_APPLE_PASSWORD)")
  fi
  # The notary key path is used whenever FAMCLAW_NOTARY_KEY is set, so its
  # triple must be complete even if an Apple-ID fallback is also configured.
  if [ -n "${FAMCLAW_NOTARY_KEY:-}" ]; then
    [ -n "${FAMCLAW_NOTARY_KEY_ID:-}" ] || missing+=("FAMCLAW_NOTARY_KEY_ID (goes with FAMCLAW_NOTARY_KEY)")
    [ -n "${FAMCLAW_NOTARY_ISSUER_ID:-}" ] || missing+=("FAMCLAW_NOTARY_ISSUER_ID (goes with FAMCLAW_NOTARY_KEY)")
  fi
  if [ -n "${FAMCLAW_APPLE_ID:-}" ] && [ -z "${FAMCLAW_NOTARY_KEY:-}" ] && [ -z "${FAMCLAW_APPLE_PASSWORD:-}" ]; then
    missing+=("FAMCLAW_APPLE_PASSWORD (goes with FAMCLAW_APPLE_ID)")
  fi
  if [ "${#missing[@]}" -gt 0 ]; then
    {
      echo "ERROR: darwin signing credentials are missing or incomplete:"
      for v in "${missing[@]}"; do echo "  - $v"; done
      echo ""
      echo "Refusing to build/publish an unsigned or ad-hoc signed macOS binary:"
      echo "  * unsigned/ad-hoc arm64 binaries are killed on launch (exit 137, issue #311)"
      echo "  * non-notarized binaries are blocked by Gatekeeper after a browser download (issue #214)"
      echo ""
      echo "Set the variables above as GitHub repository secrets (same names) and re-run"
      echo "the release. One-time credential setup + the full release-day checklist:"
      echo "docs/RELEASE.md"
    } >&2
    return 1
  fi
  return 0
}

if [ "${1:-}" = "--check" ]; then
  check_credentials
  echo "Darwin signing credentials present."
  exit 0
fi

[ $# -ge 1 ] || die "usage: $0 <darwin-binary> | --check"
BIN="$1"
[ -f "$BIN" ] || die "binary not found: $BIN"
command -v codesign >/dev/null 2>&1 || die "codesign not available — this script must run on a macOS runner"

# Fail loud before doing any work.
check_credentials || exit 1

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

# ── Keychain with the Developer ID Application identity ─────────────────────
KC="$TMP_DIR/famclaw.keychain"
KP=$(openssl rand -hex 16)
security create-keychain -p "$KP" "$KC"

b64_to_file() { # $1 = base64 payload, $2 = output file; tolerates wrapped input
  if command -v python3 >/dev/null 2>&1; then
    printf '%s' "$1" | python3 -c 'import base64, sys; sys.stdout.buffer.write(base64.b64decode(sys.stdin.buffer.read()))' > "$2"
  else
    base64 -d > "$2" <<< "$1" 2>/dev/null || base64 -D > "$2" <<< "$1"
  fi
}

b64_to_file "$FAMCLAW_APPLE_P12" "$TMP_DIR/identity.p12"
security import "$TMP_DIR/identity.p12" \
  -P "$FAMCLAW_APPLE_PASSPHRASE" -T /usr/bin/codesign -k "$KC" \
  || die "failed to import the Developer ID identity (bad FAMCLAW_APPLE_P12 or FAMCLAW_APPLE_PASSPHRASE?)"

IDENTITY=$(security find-identity -v -p codesigning "$KC" \
  | grep -o '"Developer ID Application: [^"]*"' | head -1 | tr -d '"' \
  || true)
[ -n "$IDENTITY" ] || die "no 'Developer ID Application' identity found after import — FAMCLAW_APPLE_P12 must contain the Developer ID Application certificate + key"

# ── Sign (hardened runtime is required for notarization) ────────────────────
echo "Signing $BIN with: $IDENTITY"
codesign --force --options runtime --timestamp \
  --keychain "$KC" --sign "$IDENTITY" "$BIN" \
  || die "codesign failed on $BIN"
codesign --verify --strict "$BIN" || die "codesign verification failed on $BIN"

# ── Notarize ─────────────────────────────────────────────────────────────────
echo "Notarizing $BIN ..."
NOTARY_ARGS=(submit "$BIN" --team-id "$FAMCLAW_APPLE_TEAM_ID" --wait)
if [ -n "${FAMCLAW_NOTARY_KEY:-}" ]; then
  b64_to_file "$FAMCLAW_NOTARY_KEY" "$TMP_DIR/notary.p12"
  NOTARY_ARGS+=(--key-path "$TMP_DIR/notary.p12" --key-id "$FAMCLAW_NOTARY_KEY_ID" --issuer-id "$FAMCLAW_NOTARY_ISSUER_ID")
else
  NOTARY_ARGS+=(--apple-id "$FAMCLAW_APPLE_ID" --password "$FAMCLAW_APPLE_PASSWORD")
fi
xcrun notarytool "${NOTARY_ARGS[@]}" \
  || die "notarization failed for $BIN — read the log URL notarytool printed (xcrun notarytool log <id>)"

# ── Staple the ticket + final verification ──────────────────────────────────
echo "Stapling notarization ticket to $BIN"
xcrun stapler staple "$BIN" || die "stapling failed for $BIN"
xcrun stapler verify "$BIN" || die "stapler verification failed for $BIN"
codesign --verify --strict "$BIN" || die "final codesign verification failed for $BIN"
echo "OK: $BIN is Developer-ID signed, notarized, and stapled."
