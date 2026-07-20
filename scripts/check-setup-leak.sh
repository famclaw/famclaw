#!/usr/bin/env bash
# Narrow leak-check for private-tooling strings: llama-swap, litellm

# Set to exit on any error
set -e

# Define the patterns to search for (case-insensitive)
PATTERNS="(llama-swap|litellm)"

# Get list of tracked files in the specified directories, excluding this script
FILES=$(git ls-files | grep -E '^(internal/|cmd/|scripts/|\.github/workflows/)' | grep -v 'scripts/check-setup-leak.sh')

# If no files, exit 0 (should not happen in a normal repo)
if [ -z "$FILES" ]; then
    echo "OK: no private-tooling references found"
    exit 0
fi

# Search for the patterns in the files
if echo "$FILES" | xargs grep -iE "\b$PATTERNS\b" /dev/null; then
    # If grep finds a match, it will output the file:line and we exit 1
    exit 1
else
    echo "OK: no private-tooling references found"
    exit 0
fi