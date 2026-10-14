#!/usr/bin/env bash
# End-to-end test for check-private-details.sh.
#
# Builds throwaway git repos in a temp dir, installs the guard, commits fixture
# files (leak-shaped values plus synthetic values that must stay clean), runs the
# guard, and asserts the exact pass/fail set. It then re-checks this file itself
# under the guard, and the guard under a non-excluded path, proving both scripts
# stay clean under their own checks.
#
# Every sensitive literal below is assembled from split fragments at runtime, so
# no fixture value, internal project name, or home path ever appears as a
# contiguous string in this file: the harness is itself clean under the guard it
# tests.
#
# Run: bash .github/scripts/test-check-private-details.sh
set -uo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
GUARD="$SCRIPT_DIR/check-private-details.sh"
[ -f "$GUARD" ] || { echo "FAIL guard script not found: $GUARD" >&2; exit 2; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail_count=0
fail() { echo "ASSERT FAIL: $1" >&2; fail_count=$((fail_count + 1)); }

# --- sensitive literals, assembled from fragments (see header) ---
ip='192.168.1.';       ip="${ip}77"                      # unlisted 192.168 address
exip='192.168.';       exip="${exip}1.50"                # documented example address
hp='/home/';           hp="${hp}dep/notes.md"            # personal home path
hp2='/home/';          hp2="${hp2}runner/ci job"         # placeholder home path
up='/Users/';          up="${up}you/notes"               # placeholder home path
c1='first';            c1="${c1}mate"                    # internal project name (1)
c2='fle';              c2="${c2}et"                      # internal project name (2)
c3='no-mis';           c3="${c3}takes"                   # internal project name (3)
c4='cr';               c4="${c4}ew"                      # internal project name (4)
sk='sk-';              sk="${sk}abcdefghij1234K9"        # 20-char sk- style key
fake='sk-fake';        fake="${fake}1234567890abcd"      # synthetic sk- style key
gh='ghp_';             gh="${gh}1234567890abcdef12"      # 18-char ghp style token
jwt='eyJabc123.';      jwt="${jwt}def456.ghi789"         # 3-part JWT
bear='Bearer ';        bear="${bear}tok1234567890abcd"   # Bearer header value
pem='-----BEGIN RSA P'; pem="${pem}RIVATE KEY-----"      # PEM block header
aws='AKIA';            aws="${aws}1234567890ABCDEF"      # AWS style key id
kv='passwor';          kv="${kv}d: \"abc123real\""       # key/value credential line

# --- repo 1: every leak line must fail, every clean line must pass ---
repo="$tmp/leakrepo"; mkdir -p "$repo/.github/scripts"
git -C "$repo" init -q
git -C "$repo" config user.email guard@test.local
git -C "$repo" config user.name guard
cp "$GUARD" "$repo/.github/scripts/check-private-details.sh"
cat > "$repo/leaks.txt" <<EOF
host ${ip} is here
path ${hp} backup
model ${c1} notes
gateway ${c2} notes
reviewer ${c3} passed
the ${c4} stood by
key ${sk} stored
token ${gh} stored
${jwt} pasted
${bear} sent
${kv}
${pem}
${aws} id
EOF
cat > "$repo/clean.txt" <<EOF
${exip} documented example
${hp2}
${up}
password: ""
api_key: "sk-..."
token: "${fake}"
"Bearer <REDACTED>"
test run with your-key-here
Bot FAKE_DISCORD_BOT_SHORT
sk-openai-key and sk-yaml-value
10.0.0.42 and 172.16.5.5
EOF
git -C "$repo" add -A
git -C "$repo" commit -qm fixtures

out=$(cd "$repo" && bash .github/scripts/check-private-details.sh 2>&1); rc=$?
[ "$rc" -eq 1 ] || fail "expected exit 1, got $rc (output: $out)"
for n in 1 2 3 4 5 6 7 8 9 10 11 12 13; do
  grep -q "leaks.txt:$n:" <<<"$out" || fail "leaks.txt line $n not flagged"
done
for n in 1 2 3 4 5 6 7 8 9 10 11; do
  grep -q "clean.txt:$n:" <<<"$out" && fail "clean.txt line $n wrongly flagged"
done
grep -q "check-private-details.sh" <<<"$out" && fail "guard script flagged itself"

# --- repo 2: this harness alone, under the guard, must be clean ---
repo2="$tmp/selfrepo"; mkdir -p "$repo2"
git -C "$repo2" init -q
git -C "$repo2" config user.email guard@test.local
git -C "$repo2" config user.name guard
cp "$0" "$repo2/test-check-private-details.sh"
git -C "$repo2" add -A
git -C "$repo2" commit -qm selfcheck
out2=$(cd "$repo2" && bash "$GUARD" 2>&1); rc2=$?
[ "$rc2" -eq 0 ] || fail "harness not clean under the guard: $out2"
grep -q "FAIL" <<<"$out2" && fail "harness produced FAIL lines: $out2"
grep -q "^OK" <<<"$out2" || fail "harness self-check missing OK line: $out2"

# --- repo 3: the guard itself under a NON-excluded path, must still be clean ---
repo3="$tmp/guardrepo"; mkdir -p "$repo3/tools"
git -C "$repo3" init -q
git -C "$repo3" config user.email guard@test.local
git -C "$repo3" config user.name guard
cp "$GUARD" "$repo3/tools/check-private-details.sh"
git -C "$repo3" add -A
git -C "$repo3" commit -qm guardselfcheck
out3=$(cd "$repo3" && bash tools/check-private-details.sh 2>&1); rc3=$?
[ "$rc3" -eq 0 ] || fail "guard not clean when scanned: $out3"
grep -q "FAIL" <<<"$out3" && fail "guard produced FAIL lines on itself: $out3"
grep -q "^OK" <<<"$out3" || fail "guard self-check missing OK line: $out3"

if [ "$fail_count" -eq 0 ]; then
  echo "OK - guard behavior, harness self-cleanliness, and guard self-cleanliness all verified"
  exit 0
fi
echo "FAIL - $fail_count assertion(s) failed" >&2
exit 1
