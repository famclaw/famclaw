#!/usr/bin/env bash
# .github/scripts/test-check-private-details.sh
#
# Executes the private-details leak guard (.github/scripts/check-private-details.sh)
# against synthetic fixtures in a throwaway git repo, proving:
#   1. a clean repo passes (tracked-files-only baseline),
#   2. a tracked-file leak still fails (guard behavior preserved),
#   3. a leak in PR title / PR body metadata FAILS the guard even though the
#      equivalent tracked-files-only check passes (the metadata gap this guards),
#   4. documented placeholders and allowed example addresses still pass,
#   5. failure output names label/line/pattern only — never the offending value.
#
# NO-LEAK: the fixtures are real patterns (a home path, a planted PRIVATE_IP
# sample) but obviously synthetic (fixtureperson / TEST-NET-style, never a host
# here). They are built at runtime via obf() so this harness file itself — a
# tracked file the guard scans — matches none of its own patterns.
#
# Requires: bash, git. Run: .github/scripts/test-check-private-details.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD="${SCRIPT_DIR}/check-private-details.sh"

if [ ! -f "$GUARD" ]; then
  echo "FAIL: guard script not found at $GUARD" >&2
  exit 1
fi

# --- test helpers -----------------------------------------------------------
pass=0
fail=0
ok()    { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad()   { printf '  \033[31m✗\033[0m %s\n' "$1" >&2; fail=$((fail + 1)); }
assert_zero()    { if [ "$2" -eq 0 ]; then ok "$1"; else bad "$1 (expected 0, got $2)"; fi; }
assert_nonzero() { if [ "$2" -ne 0 ]; then ok "$1"; else bad "$1 (expected non-zero, got $2)"; fi; }
assert_contains() {
  if printf '%s' "$3" | grep -qF "$2"; then ok "$1"; else bad "$1 (missing: $2)"; fi
}
assert_not_contains() {
  if printf '%s' "$3" | grep -qF "$2"; then bad "$1 (found: $2)"; else ok "$1"; fi
}

obf() { printf '%s' "$1" | tr -d 'X'; }

CLEANUP=()
# Invoked indirectly via the trap below. SC2329 (function never invoked) and
# SC2317 (body unreachable, flagged by 0.9.0) are both false positives here.
# shellcheck disable=SC2317,SC2329
cleanup() { for p in "${CLEANUP[@]:-}"; do rm -rf "$p"; done; }
trap cleanup EXIT

# Synthetic leak values (see NO-LEAK note above). X sentinel is stripped by tr.
LEAK_IP=$(obf '192.168X.127.8')
LEAK_PATH=$(obf '/hoXme/fixtureperson/projects/private-notes.md')
LEAK_IP_ALT=$(obf '192.168X.127.9')

# --- fixture repo -----------------------------------------------------------
REPO="$(mktemp -d)"
CLEANUP+=("$REPO")
git -C "$REPO" init -q
cat > "$REPO/README.md" <<'EOF'
Placeholder paths like /home/user/app and /Users/you/app and CI's /home/runner
are fine, as are the documented example addresses 192.168.0.1 and 192.168.1.50.
EOF
git -C "$REPO" add README.md

# run_guard [PR_TITLE_FILE] [PR_BODY_FILE] — runs the guard inside the fixture
# repo, captures combined output in GUARD_OUT and the exit code in RC.
run_guard() {
  GUARD_OUT=""
  local title="${1:-}" body="${2:-}"
  RC=0
  GUARD_OUT=$( cd "$REPO" && \
    PR_TITLE_FILE="$title" PR_BODY_FILE="$body" bash "$GUARD" 2>&1 ) || RC=$?
}

# --- 1. clean repo passes ----------------------------------------------------
run_guard
assert_zero "clean repo passes tracked-files-only check" "$RC"

# --- 2. tracked-file leak still fails ----------------------------------------
printf 'router host: %s, notes at %s\n' "$LEAK_IP" "$LEAK_PATH" > "$REPO/config.md"
git -C "$REPO" add config.md
run_guard
assert_nonzero "tracked-file leak fails the guard" "$RC"
assert_contains "tracked leak names HOME_PATH pattern" "(HOME_PATH)" "$GUARD_OUT"
assert_contains "tracked leak names PRIVATE_IP pattern" "(PRIVATE_IP)" "$GUARD_OUT"
assert_not_contains "tracked leak output hides the IP" "$LEAK_IP" "$GUARD_OUT"
assert_not_contains "tracked leak output hides the path" "$LEAK_PATH" "$GUARD_OUT"
rm "$REPO/config.md"
git -C "$REPO" reset -q -- config.md

# --- 3. PR metadata leak fails where tracked-files-only passes ---------------
printf 'Fix %s exposure on %s\n' "$LEAK_PATH" "$LEAK_IP_ALT" > "$REPO/leak-title.txt"
printf 'Repro: ssh %s then read %s\n' "$LEAK_IP_ALT" "$LEAK_PATH" > "$REPO/leak-body.txt"
run_guard "$REPO/leak-title.txt" "$REPO/leak-body.txt"
assert_nonzero "PR metadata leak fails the guard" "$RC"
assert_contains "metadata leak labels PR title" "PR title:" "$GUARD_OUT"
assert_contains "metadata leak labels PR body" "PR body:" "$GUARD_OUT"
assert_not_contains "metadata leak output hides the IP" "$LEAK_IP_ALT" "$GUARD_OUT"
assert_not_contains "metadata leak output hides the path" "$LEAK_PATH" "$GUARD_OUT"

# title-only leak (body clean) must still fail
printf 'Clean body mentions /home/user only.\n' > "$REPO/clean-body.txt"
run_guard "$REPO/leak-title.txt" "$REPO/clean-body.txt"
assert_nonzero "PR title-only leak fails the guard" "$RC"

# same repo with no metadata files passes (the gap being closed)
run_guard
assert_zero "same repo passes without PR metadata (gap baseline)" "$RC"

# --- 4. allow-list regression ------------------------------------------------
printf 'Copy from /home/user/notes, /Users/you/app, /home/runner/work; peers: 192.168.0.1, 192.168.1.50, 192.168.1.10\n' > "$REPO/allowed-title.txt"
printf 'Docs example 192.168.0.1 and placeholder /Users/USER/docs stay allowed.\n' > "$REPO/allowed-body.txt"
run_guard "$REPO/allowed-title.txt" "$REPO/allowed-body.txt"
assert_zero "placeholders + documented examples still pass" "$RC"

# --- 5. missing / unset metadata files are skipped, not fatal ----------------
run_guard "/nonexistent-title-file" "$REPO/allowed-body.txt"
assert_zero "missing title file is skipped" "$RC"

# --- summary ------------------------------------------------------------------
printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
