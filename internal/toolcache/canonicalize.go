package toolcache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// CanonicalArgsHash returns a stable sha256 hex digest of canonicalized args.
// Used as the cache dedup key. Best-effort canonicalization — floating-point
// edge cases or unusual escapes may cause false misses on logically-equivalent
// inputs. Acceptable for a family bot; occasional re-fetch is fine.
func CanonicalArgsHash(args map[string]any) (string, error) {
	canonicalBytes, err := canonicalizeArgs(args)
	if err != nil {
		return "", fmt.Errorf("canonicalize args: %w", err)
	}
	hash := sha256.Sum256(canonicalBytes)
	return hex.EncodeToString(hash[:]), nil
}

// canonicalizeArgs renders args as JSON with sorted top-level + nested map
// keys, no whitespace. Lists preserve their original order. Used by both
// CanonicalArgsHash and tests directly.
func canonicalizeArgs(args map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, args); err != nil {
		return nil, fmt.Errorf("write canonical: %w", err)
	}
	return buf.Bytes(), nil
}

// writeCanonical walks v recursively, writing JSON with sorted map keys.
// Uses json.Marshal for leaf values (which does not append newlines).
func writeCanonical(buf *bytes.Buffer, v any) error {
	switch vv := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(vv))
		for k := range vv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return fmt.Errorf("marshal key %q: %w", k, err)
			}
			buf.Write(kb)
			buf.WriteByte(':')
			if err := writeCanonical(buf, vv[k]); err != nil {
				return fmt.Errorf("write value for %q: %w", k, err)
			}
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, elem := range vv {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, elem); err != nil {
				return fmt.Errorf("write element %d: %w", i, err)
			}
		}
		buf.WriteByte(']')
	default:
		// Leaf: json.Marshal (no trailing newline, unlike json.Encoder).
		b, err := json.Marshal(vv)
		if err != nil {
			return fmt.Errorf("marshal value: %w", err)
		}
		buf.Write(b)
	}
	return nil
}
