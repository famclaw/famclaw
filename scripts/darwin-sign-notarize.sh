#!/usr/bin/env bash
# FamClaw darwin code-signing + notarization (release gate).
#
# Invoked from the goreleaser builds.hooks.post entry in .goreleaser.yaml for
# every darwin binary, and as `--check` by the release workflow preflight
# step.
#
# Why signing + notarization exists (issues #311, #214):
#   - an *unsigned* arm64 Mach-O binary is killed by the kernel on launch:
#     exit 137, no output — indistinguishable from a corrupt binary to the
#     user;
#   - a binary that is not signed with a Developer ID Application
#     certificate and notarized is blocked by Gatekeeper after a browser
#     download (com.apple.quarantine), so non-technical users cannot
#     install it at all.
#
# Two release paths — the preflight `--check` step and this hook apply the
# same credential decision, so they always agree:
#   - credentials complete: the binary is signed with the Developer ID
#     Application certificate, notarized, and stapled.
#   - credentials missing or partial: a WARNING names each missing
#     FAMCLAW_* credential and the release falls back to the pre-v0.13.0
#     ad-hoc path: the binary ships ad-hoc-signed. It still runs on Apple
#     Silicon (ad-hoc signing satisfies the kernel, #311), but a
#     browser-downloaded copy is blocked by Gatekeeper on first run until
#     the user right-clicks -> Open (#214).
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
#   darwin-sign-notarize.sh <darwin-binary>   sign + notarize + staple + verify;
#                                             ad-hoc fallback when credentials
#                                             are missing or partial
#   darwin-sign-notarize.sh --check           warn when credentials are missing
#                                             or partial, then exit 0 (the
#                                             preflight never blocks the
#                                             release on them)
set -euo pipefail

die() { echo "ERROR: $*" >&2; exit 1; }

# Prints the missing darwin signing credentials, one per line (empty when the
# set is complete). Never fails: absent credentials are the ad-hoc fallback,
# not an error.
list_missing_credentials() {
  local missing_creds=()
  [ -n "${FAMCLAW_APPLE_P12:-}" ] || missing_creds+=("FAMCLAW_APPLE_P12")
  [ -n "${FAMCLAW_APPLE_PASSPHRASE:-}" ] || missing_creds+=("FAMCLAW_APPLE_PASSPHRASE")
  [ -n "${FAMCLAW_APPLE_TEAM_ID:-}" ] || missing_creds+=("FAMCLAW_APPLE_TEAM_ID")
  if [ -z "${FAMCLAW_NOTARY_KEY:-}${FAMCLAW_APPLE_ID:-}" ]; then
    missing_creds+=("FAMCLAW_NOTARY_KEY + FAMCLAW_NOTARY_KEY_ID + FAMCLAW_NOTARY_ISSUER_ID (or FAMCLAW_APPLE_ID + FAMCLAW_APPLE_PASSWORD)")
  fi
  # The notary key path is used whenever FAMCLAW_NOTARY_KEY is set, so its
  # triple must be complete even if an Apple-ID fallback is also configured.
  if [ -n "${FAMCLAW_NOTARY_KEY:-}" ]; then
    [ -n "${FAMCLAW_NOTARY_KEY_ID:-}" ] || missing_creds+=("FAMCLAW_NOTARY_KEY_ID (goes with FAMCLAW_NOTARY_KEY)")
    [ -n "${FAMCLAW_NOTARY_ISSUER_ID:-}" ] || missing_creds+=("FAMCLAW_NOTARY_ISSUER_ID (goes with FAMCLAW_NOTARY_KEY)")
  fi
  if [ -n "${FAMCLAW_APPLE_ID:-}" ] && [ -z "${FAMCLAW_NOTARY_KEY:-}" ] && [ -z "${FAMCLAW_APPLE_PASSWORD:-}" ]; then
    missing_creds+=("FAMCLAW_APPLE_PASSWORD (goes with FAMCLAW_APPLE_ID)")
  fi
  if [ "${#missing_creds[@]}" -gt 0 ]; then
    printf '%s\n' "${missing_creds[@]}"
  fi
}

warn_adhoc_fallback() {
  # $1 = newline-separated list of the missing credentials
  {
    echo "WARNING: darwin signing credentials are missing or incomplete:"
    while IFS= read -r v; do
      echo "  - $v"
    done <<< "$1"
    echo ""
    echo "The darwin release binaries will ship AD-HOC SIGNED (the pre-v0.13.0 fallback):"
    echo "  * ad-hoc signing satisfies the kernel, so the binaries still run on Apple"
    echo "    Silicon (exit 137, issue #311, only hits *unsigned* binaries);"
    echo "  * a browser-downloaded binary is blocked by Gatekeeper on first run"
    echo "    (issue #214) — workaround: right-click the binary -> Open (-> Open again"
    echo "    in the dialog), or 'xattr -d com.apple.quarantine <binary>'."
    echo ""
    echo "To ship Developer ID-signed + notarized binaries instead, set the missing"
    echo "variables as GitHub repository secrets (same names) and re-run the release."
    echo "One-time credential setup + the full release-day checklist: docs/RELEASE.md"
  } >&2
}

if [ "${1:-}" = "--check" ]; then
  missing="$(list_missing_credentials)"
  if [ -n "$missing" ]; then
    warn_adhoc_fallback "$missing"
  else
    echo "Darwin signing credentials present."
  fi
  exit 0
fi

[ $# -ge 1 ] || die "usage: $0 <darwin-binary> | --check"
BIN="$1"
[ -f "$BIN" ] || die "binary not found: $BIN"
command -v codesign >/dev/null 2>&1 || die "codesign not available — this script must run on a macOS runner"

# No usable Developer ID credentials: fall back to the pre-v0.13.0 ad-hoc
# path instead of failing the release. The macOS linker already ad-hoc signs
# the binary; the explicit re-sign mirrors the v0.12.0 hook and makes the
# fallback visible in the release log.
missing="$(list_missing_credentials)"
if [ -n "$missing" ]; then
  warn_adhoc_fallback "$missing"
  echo "Ad-hoc signing $BIN (no Developer ID credentials configured)"
  codesign --force -s - "$BIN" || die "ad-hoc codesign failed on $BIN"
  codesign --verify --strict "$BIN" || die "ad-hoc codesign verification failed on $BIN"
  echo "OK: $BIN is ad-hoc signed (no notarization)."
  exit 0
fi

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
