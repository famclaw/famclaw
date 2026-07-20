#!/bin/bash
# Script to check for private setup leaks in internal/ and cmd/ directories.
# Exits with 0 if no leaks found, 1 otherwise.

# Define the patterns to search for (case-insensitive)
PATTERNS="nemotron|qwen3|qwen2.5|gpt-oss|deepseek|llama-swap|litellm|192\.168\.|localhost:80[0-9][0-9]|on Mac|Spark"

# Find all Go files in internal/ and cmd/ directories
FILES=$(find internal/ cmd -type f -name "*.go" 2>/dev/null)

LEAK_FOUND=0

for file in $FILES; do
    if grep -iE "$PATTERNS" "$file" > /dev/null; then
        echo "Leak found in $file:"
        grep -iE "$PATTERNS" "$file"
        LEAK_FOUND=1
    fi
done

if [ $LEAK_FOUND -eq 1 ]; then
    echo "ERROR: Private setup leaks detected. Please remove them."
    exit 1
else
    echo "OK: No private setup leaks found in internal/ and cmd/."
    exit 0
fi
