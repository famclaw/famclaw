#!/usr/bin/env bash
# Fail if a tracked file contains private developer details.
#
# A maintainer's real home LAN address once sat in a public repo for over a week,
# past every human review. This is the mechanical backstop for that class of leak.
#
# Allowed: generic placeholder home paths (/home/user, /Users/you, CI's /home/runner),
# the example addresses this project documents, and the 10.x / 172.16-31.x ranges,
# which are widely used as synthetic test data.
set -uo pipefail

PLACEHOLDER='/(home|Users)/(user|username|youruser|you|USER|runner)(/|$)'
HOME_PATH='/(home|Users)/[A-Za-z_][A-Za-z0-9_.-]*/'
# Any 192.168.x address is flagged EXCEPT the documented example addresses listed
# below. Deliberately an allow-list of generic placeholders, never a list of real
# machines: this file is public, so naming the addresses to hide would publish them.
# Adding a genuinely new documentation example means editing ALLOWED_IP in a PR -
# a small, reviewable, deliberate act, which is the point.
PRIVATE_IP='192\.168\.[0-9]{1,3}\.[0-9]{1,3}'
ALLOWED_IP='192\.168\.(0\.1|1\.(1|2|10|50|100))([^0-9]|$)'

exit_code=0
while IFS= read -r f; do
  case "$f" in
    .github/workflows/*|.github/scripts/check-private-details.sh) continue ;;
  esac
  [ -f "$f" ] || continue
  hits=$(
    {
      grep -nE "$HOME_PATH" "$f" 2>/dev/null | grep -vE "$PLACEHOLDER"
      grep -nE "$PRIVATE_IP" "$f" 2>/dev/null | grep -vE "$ALLOWED_IP"
    } | sort -u
  )
  if [ -n "$hits" ]; then
    while IFS= read -r line; do
      echo "FAIL $f:$line"
    done <<< "$hits"
    exit_code=1
  fi
done < <(git ls-files)

if [ "$exit_code" -eq 0 ]; then
  echo "OK - no private developer details in tracked files"
fi
exit "$exit_code"
