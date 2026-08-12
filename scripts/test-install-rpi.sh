#!/usr/bin/env bash
# scripts/test-install-rpi.sh
#
# Validates the atomic binary-install logic in scripts/install-rpi.sh
# (issue #317) by exercising its `install_binary` function against a throwaway
# local HTTP server that mimics a GitHub release endpoint.
#
# Why this exists: shellcheck cannot catch the original defect (a variable
# mismatch where curl wrote to an undefined $FAMCLAW_TMP while the mktemp'd
# file lived in $TMP_PATH). Only by *executing* the real install code path
# against a real HTTP server can we prove a failed/truncated download leaves a
# running binary untouched.
#
# Cases:
#   1. success  — HTTP 200 + known binary → installed, executable, runs.
#   2. http500  — HTTP 500 → install fails; existing binary untouched.
#   3. empty    — HTTP 200 + zero bytes → install fails (non-empty guard);
#                 existing binary untouched.
#
# Requires: bash, curl, python3. Run: ./scripts/test-install-rpi.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALLER="${SCRIPT_DIR}/install-rpi.sh"

if [ ! -f "$INSTALLER" ]; then
  echo "FAIL: install-rpi.sh not found at $INSTALLER" >&2
  exit 1
fi

# --- test helpers -----------------------------------------------------------
pass=0
fail=0
ok()    { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad()   { printf '  \033[31m✗\033[0m %s\n' "$1" >&2; fail=$((fail + 1)); }
assert_eq()       { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (got '$2', want '$3')"; fi; }
assert_zero()     { if [ "$2" -eq 0 ]; then ok "$1"; else bad "$1 (expected 0, got $2)"; fi; }
assert_nonzero()  { if [ "$2" -ne 0 ]; then ok "$1"; else bad "$1 (expected non-zero, got $2)"; fi; }
assert_file_eq()  { if cmp -s "$2" "$3"; then ok "$1"; else bad "$1 (files differ)"; fi; }
assert_exists()   { if [ -e "$2" ]; then ok "$1"; else bad "$1 (missing: $2)"; fi; }
assert_absent()   { if [ -e "$2" ]; then bad "$1 (still present: $2)"; else ok "$1"; fi; }
assert_executable() { if [ -x "$2" ]; then ok "$1"; else bad "$1 (not executable)"; fi; }

CLEANUP=()
SRV_PID=""
cleanup() {
  if [ -n "$SRV_PID" ]; then kill "$SRV_PID" 2>/dev/null || true; fi
  for p in "${CLEANUP[@]:-}"; do rm -rf "$p"; done
}
trap cleanup EXIT

# --- source the installer (defines install_binary, does NOT run main) --------
# shellcheck source=/dev/null
# shellcheck disable=SC1090
. "$INSTALLER"

if ! type install_binary >/dev/null 2>&1; then
  echo "FAIL: install_binary not defined after sourcing $INSTALLER" >&2
  exit 1
fi

# --- fake release server ----------------------------------------------------
# Serves a known binary on /famclaw (HTTP 200, with Content-Length), an empty
# body on /famclaw-empty (HTTP 200), and HTTP 500 for anything else.
RELEASE_DIR="$(mktemp -d)"
CLEANUP+=("$RELEASE_DIR")
cat > "$RELEASE_DIR/famclaw" <<'EOF'
#!/bin/sh
# A known, executable "release artifact" for the test.
echo "famclaw-test-binary"
EOF
chmod +x "$RELEASE_DIR/famclaw"

SRV_SCRIPT="$(mktemp)"
CLEANUP+=("$SRV_SCRIPT")
# Interpolate the release dir path into the Python server (single-quoted
# Python, path injected via env to avoid quoting issues).
cat > "$SRV_SCRIPT" <<'PY'
import http.server
import os

RELEASE_DIR = os.environ["RELEASE_DIR"]


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/famclaw":
            path = os.path.join(RELEASE_DIR, "famclaw")
            with open(path, "rb") as fh:
                body = fh.read()
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        elif self.path == "/famclaw-empty":
            self.send_response(200)
            self.send_header("Content-Length", "0")
            self.end_headers()
        else:
            self.send_response(500)
            self.send_header("Content-Length", "0")
            self.end_headers()

    def log_message(self, *args):
        pass


httpd = http.server.HTTPServer(("127.0.0.1", 0), Handler)
# Print the bound port on stdout so the parent can read it.
print(httpd.server_address[1], flush=True)
httpd.serve_forever()
PY

SRV_OUT="$(mktemp)"
CLEANUP+=("$SRV_OUT")
RELEASE_DIR="$RELEASE_DIR" python3 "$SRV_SCRIPT" >"$SRV_OUT" 2>/dev/null &
SRV_PID=$!

# Wait for the server to publish its ephemeral port.
PORT=""
for _ in $(seq 1 100); do
  PORT="$(cat "$SRV_OUT" 2>/dev/null || true)"
  [ -n "$PORT" ] && break
  sleep 0.05
done
if [ -z "$PORT" ]; then
  echo "FAIL: fake release server did not start (python3 output: $(cat "$SRV_OUT" 2>/dev/null))" >&2
  exit 1
fi
RELEASE_BASE="http://127.0.0.1:${PORT}"
echo "Using fake release server at $RELEASE_BASE"

# --- Case 1: success (HTTP 200 + known binary) ------------------------------
echo ""
echo "Case 1: success (HTTP 200 + known binary)"
WORK1="$(mktemp -d)"
CLEANUP+=("$WORK1")
rc=0
if install_binary "$WORK1" "famclaw" "${RELEASE_BASE}/famclaw"; then rc=0; else rc=$?; fi
assert_zero    "install returns 0 on HTTP 200" "$rc"
assert_exists  "target binary was created" "$WORK1/famclaw"
assert_file_eq "installed bytes match the release artifact" "$WORK1/famclaw" "$RELEASE_DIR/famclaw"
assert_executable "installed binary is executable" "$WORK1/famclaw"
installed_marker="$(sh "$WORK1/famclaw" 2>/dev/null || true)"
assert_eq      "installed binary runs and prints its marker" "famclaw-test-binary" "$installed_marker"
leftover="$(find "$WORK1" -maxdepth 1 -name '.famclaw.tmp.*' | wc -l | tr -d ' ')"
assert_eq    "no leftover temp files after success" "0" "$leftover"

# --- Case 2: failure (HTTP 500) leaves existing binary untouched ------------
echo ""
echo "Case 2: failure (HTTP 500) leaves existing binary untouched"
WORK2="$(mktemp -d)"
CLEANUP+=("$WORK2")
printf 'OLD-FAMLAW-BINARY-UNTOUCHED' > "$WORK2/famclaw"
chmod +x "$WORK2/famclaw"
before_sha="$(sha256sum "$WORK2/famclaw" | cut -d' ' -f1)"
rc=0
if install_binary "$WORK2" "famclaw" "${RELEASE_BASE}/famclaw-bad"; then rc=0; else rc=$?; fi
assert_nonzero "install returns non-zero on HTTP 500" "$rc"
after_sha="$(sha256sum "$WORK2/famclaw" | cut -d' ' -f1)"
assert_eq    "existing binary SHA256 unchanged" "$before_sha" "$after_sha"
assert_file_eq "existing binary content unchanged" "$WORK2/famclaw" <(printf 'OLD-FAMLAW-BINARY-UNTOUCHED')
leftover="$(find "$WORK2" -maxdepth 1 -name '.famclaw.tmp.*' | wc -l | tr -d ' ')"
assert_eq    "no leftover temp files after 500 failure" "0" "$leftover"

# --- Case 3: failure (empty 200 body) rejected by non-empty guard -------------
echo ""
echo "Case 3: failure (empty 200 body) leaves existing binary untouched"
WORK3="$(mktemp -d)"
CLEANUP+=("$WORK3")
printf 'EXISTING-BINARY' > "$WORK3/famclaw"
chmod +x "$WORK3/famclaw"
rc=0
if install_binary "$WORK3" "famclaw" "${RELEASE_BASE}/famclaw-empty"; then rc=0; else rc=$?; fi
assert_nonzero "install returns non-zero on empty body" "$rc"
actual="$(cat "$WORK3/famclaw" 2>/dev/null || true)"
assert_eq      "existing binary content unchanged" "EXISTING-BINARY" "$actual"
leftover="$(find "$WORK3" -maxdepth 1 -name '.famclaw.tmp.*' | wc -l | tr -d ' ')"
assert_eq    "no leftover temp files after empty-body failure" "0" "$leftover"

# --- Summary ----------------------------------------------------------------
echo ""
echo "────────────────────────────────────────────────────"
echo " install-rpi.sh atomic-install tests: ${pass} passed, ${fail} failed"
echo "────────────────────────────────────────────────────"
if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "ALL PASS"
