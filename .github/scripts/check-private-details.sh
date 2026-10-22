#!/usr/bin/env bash
# Fail if a tracked file or PR metadata contains private developer details.
#
# A maintainer's real home LAN address once sat in a public repo for over a week,
# past every human review. This is the mechanical backstop for that class of leak.
#
# Allowed: generic placeholder home paths (/home/user, /Users/you, CI's /home/runner),
# the example addresses this project documents, and the 10.x / 172.16-31.x ranges,
# which are widely used as synthetic test data.
#
# PR metadata: on a pull_request run, ci.yml writes the PR title and body to files
# and points at them via PR_TITLE_FILE / PR_BODY_FILE. Prose in PR metadata never
# lands in a tracked file, so the tracked-files loop below cannot see it — a leak
# there survives every later scan. No separate pass over the PR diff is needed:
# every line a PR adds is part of a tracked file of the PR head commit, which this
# checkout already covers.
#
# Failure output is deliberately public-repo-safe: label + line number + pattern
# name only, never the offending text.
# Two further classes are checked (their specific names never appear as prose in
# this file, so the script stays clean under its own checks even when scanned):
#   - internal project names (CODENAME). Written as bracket-class regex only, and
#     exempted only for the multilingual NLP test corpus, where natural-language
#     words (e.g. the word for "moon" in Spanish) are not leaks.
#   - credential shapes (CREDENTIAL): a focused complement to gitleaks, which the
#     committed scanner already runs with its full default ruleset.
# The placeholder allowlist (SYNTHETIC) covers synthetic test values only:
# fake-*/test-*/xxx/changeme/your-*/sk-<placeholder>/ellipsis/empty quoted values.
# It is deliberately narrow; a real value must never need it.
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

# Internal project names. Each leading letter is a character class so this file
# never contains one of them as plaintext, and stays clean under its own checks.
CODENAME='([Ff]irstmate|[Ff]leet|[Ll]una|[Ss]park|[Pp]ragent|pr[-_]?agent|no[-_]?mistakes|[Cc]rew)'
# Credential shapes. Complements (does not duplicate) the gitleaks ruleset the
# committed scanner runs: only the shapes this repo's fixtures and docs can
# actually contain. Bearer/Bot values require a digit, so all-caps placeholder
# tokens without digits do not trip it.
CREDENTIAL='(-----BEGIN [A-Z0-9 ]*PRIVATE KEY|(AKIA|ASIA|AGPA|AIDA|ANPA|ANVA)[0-9A-Z]{16}|\bsk-[A-Za-z0-9_-]{16,}|\bgh[pousr]_[A-Za-z0-9]{16,}|\bgithub_pat_[A-Za-z0-9_]{16,}|\bxox[bao]-[A-Za-z0-9-]{10,}|eyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{4,}|(Bearer|Bot) [A-Za-z0-9._-]*[0-9][A-Za-z0-9._-]{12,}|([Pp]assword|[Ss]ecret|api[_-]?[Kk]ey|API[_-]?KEY|auth[_-]?[Tt]oken|access[_-]?[Tt]oken|bot[_-]?[Tt]oken)["'"'"']?[[:space:]]*[:=][[:space:]]*["'"'"'])'
# Synthetic/placeholder values: a line matching any of these is test data, not a
# leak. Covers: narrow placeholder words (changeme/change-me/dummy/xxx/your-*/
# fake-*/test-*/sk-<placeholder>), env-var references ("${OPENAI_API_KEY}",
# "$SECRET") which are not literal values, all-caps generated-placeholder
# convention (WILL_BE_GENERATED...), empty quoted values, "..." elisions, and the
# standard zero-entropy HS256 example JWT header from the public JWT docs.
# Deliberately narrow and case-sensitive where it matters ("Test123" is still
# flagged; "test-123" is not). A real value must never need this list.
SYNTHETIC='(changeme|change[-_]?me|dummy|xxx|your[_-][a-z0-9_]*|[Ff]ake|test[-_ ]|sk[-_](from|test|fake|dummy|example|sample|mock|dev|detect)|WILL_BE|GENERATED|\$\{[A-Za-z_][A-Za-z0-9_]*\}|\$[A-Z_]+|["'"'"']{2,3}|\.\.\.|eyJhbGciOi)'

# scan_file <label> <file>
# One FAIL per offending match: label, line number, pattern name — no value dump.
# The single check used by both the tracked-files loop and the PR-metadata path,
# so the pattern rules live in exactly one place.
scan_file() {
  local label="$1" file="$2"
  local hits
  hits=$(
    {
      grep -nE "$HOME_PATH" "$file" 2>/dev/null | grep -vE "$PLACEHOLDER" \
        | sed 's/^\([0-9][0-9]*\):.*/\1:(HOME_PATH)/'
      grep -nE "$PRIVATE_IP" "$file" 2>/dev/null | grep -vE "$ALLOWED_IP" \
        | sed 's/^\([0-9][0-9]*\):.*/\1:(PRIVATE_IP)/'
    } | sort -u
  )
  if [ -n "$hits" ]; then
    while IFS= read -r hit; do
      echo "FAIL ${label}:${hit}"
    done <<< "$hits"
    return 1
  fi
  return 0
}

exit_code=0
while IFS= read -r f; do
  case "$f" in
    .github/workflows/*|.github/scripts/check-private-details.sh) continue ;;
  esac
  [ -f "$f" ] || continue
  # HOME_PATH + PRIVATE_IP via scan_file (reusable for PR metadata)
  scan_file "$f" "$f" || exit_code=1
  # CODENAME + CREDENTIAL (tracked files only, with NLP test-corpus exemption)
  case "$f" in
    internal/classifier/testdata/*) ;;
    *)
      c_hits=$(
        {
          grep -nE "$CODENAME" "$f" 2>/dev/null | grep -vE "$SYNTHETIC" \
            | sed 's/^\([0-9][0-9]*\):.*/\1:(CODENAME)/'
          grep -nE "$CREDENTIAL" "$f" 2>/dev/null | grep -vE "$SYNTHETIC" \
            | sed 's/^\([0-9][0-9]*\):.*/\1:(CREDENTIAL)/'
        } | sort -u
      )
      if [ -n "$c_hits" ]; then
        while IFS= read -r line; do
          echo "FAIL ${f}:${line}"
        done <<< "$c_hits"
        exit_code=1
      fi
      ;;
  esac
done < <(git ls-files)

if [ -n "${PR_TITLE_FILE:-}" ] && [ -f "${PR_TITLE_FILE}" ]; then
  scan_file "PR title" "$PR_TITLE_FILE" || exit_code=1
fi
if [ -n "${PR_BODY_FILE:-}" ] && [ -f "${PR_BODY_FILE}" ]; then
  scan_file "PR body" "$PR_BODY_FILE" || exit_code=1
fi

if [ "$exit_code" -eq 0 ]; then
  echo "OK - no private developer details in tracked files or PR metadata"
fi
exit "$exit_code"
