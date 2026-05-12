package toolcache

import (
	"testing"
)

func TestCanonicalizeMapKeyOrderIndependent(t *testing.T) {
	a := map[string]any{"url": "https://x.com", "max_bytes": 1024}
	b := map[string]any{"max_bytes": 1024, "url": "https://x.com"}
	ha, err := CanonicalArgsHash(a)
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	hb, err := CanonicalArgsHash(b)
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	if ha != hb {
		t.Errorf("hashes differ for logically-equal maps: %s vs %s", ha, hb)
	}
}

func TestCanonicalizeNestedMaps(t *testing.T) {
	a := map[string]any{"k": map[string]any{"a": 1, "b": 2}}
	b := map[string]any{"k": map[string]any{"b": 2, "a": 1}}
	ha, _ := CanonicalArgsHash(a)
	hb, _ := CanonicalArgsHash(b)
	if ha != hb {
		t.Errorf("nested maps should canonicalize the same: %s vs %s", ha, hb)
	}
}

func TestCanonicalizeProducesDenseJSON(t *testing.T) {
	a := map[string]any{"k": "v", "n": 42}
	bs, err := canonicalizeArgs(a)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	want := `{"k":"v","n":42}`
	if string(bs) != want {
		t.Errorf("canonicalize = %q, want %q", string(bs), want)
	}
}

func TestCanonicalizeArraysPreserveOrder(t *testing.T) {
	a := map[string]any{"items": []any{"a", "b", "c"}}
	b := map[string]any{"items": []any{"c", "b", "a"}}
	ha, _ := CanonicalArgsHash(a)
	hb, _ := CanonicalArgsHash(b)
	if ha == hb {
		t.Error("arrays should NOT be order-independent (they preserve order)")
	}
}

func TestCanonicalArgsHashIs64HexChars(t *testing.T) {
	h, err := CanonicalArgsHash(map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if len(h) != 64 {
		t.Errorf("hash length = %d, want 64", len(h))
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-hex char %q in hash %s", c, h)
		}
	}
}

func TestCanonicalizeEmptyMap(t *testing.T) {
	bs, err := canonicalizeArgs(map[string]any{})
	if err != nil {
		t.Fatalf("canonicalize empty: %v", err)
	}
	if string(bs) != "{}" {
		t.Errorf("empty map = %q, want '{}'", string(bs))
	}
}
