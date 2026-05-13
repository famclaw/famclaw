package toolcache

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAndReadPayloadRoundTrip(t *testing.T) {
	root := t.TempDir()
	payload := []byte("hello world")
	rel, err := writePayload(context.Background(), root, "alice", "01XYZ", payload)
	if err != nil {
		t.Fatalf("writePayload: %v", err)
	}
	got, err := readPayload(root, rel, 0, len(payload))
	if err != nil {
		t.Fatalf("readPayload: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload mismatch: got %q want %q", got, payload)
	}
}

func TestWritePayloadUsesForwardSlashes(t *testing.T) {
	root := t.TempDir()
	rel, err := writePayload(context.Background(), root, "alice", "01ABC", []byte("x"))
	if err != nil {
		t.Fatalf("writePayload: %v", err)
	}
	if bytes.Contains([]byte(rel), []byte{'\\'}) {
		t.Errorf("relative path contains backslash, not portable for sqlite: %q", rel)
	}
}

func TestReadPayloadWithOffset(t *testing.T) {
	root := t.TempDir()
	payload := []byte("0123456789ABCDEF")
	rel, _ := writePayload(context.Background(), root, "alice", "01YYY", payload)
	got, err := readPayload(root, rel, 5, 5)
	if err != nil {
		t.Fatalf("readPayload: %v", err)
	}
	want := []byte("56789")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestReadPayloadBeyondEOFClips(t *testing.T) {
	root := t.TempDir()
	payload := []byte("short")
	rel, _ := writePayload(context.Background(), root, "alice", "01ZZZ", payload)
	got, err := readPayload(root, rel, 0, 1000)
	if err != nil {
		t.Fatalf("readPayload: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %q want %q", got, payload)
	}
}

func TestReadPayloadNegativeArgsClamp(t *testing.T) {
	root := t.TempDir()
	payload := []byte("hello")
	rel, _ := writePayload(context.Background(), root, "alice", "01NEG", payload)
	got, err := readPayload(root, rel, -1, -1)
	if err != nil {
		t.Fatalf("readPayload: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("negative length should clamp to 0, got len=%d", len(got))
	}
}

func TestDeletePayload(t *testing.T) {
	root := t.TempDir()
	rel, _ := writePayload(context.Background(), root, "alice", "01DEL", []byte("bye"))
	if err := deletePayload(root, rel); err != nil {
		t.Fatalf("deletePayload: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
		t.Errorf("expected file gone, got err=%v", err)
	}
}

func TestDeletePayloadIdempotent(t *testing.T) {
	root := t.TempDir()
	rel, _ := writePayload(context.Background(), root, "alice", "01TWICE", []byte("x"))
	if err := deletePayload(root, rel); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := deletePayload(root, rel); err != nil {
		t.Errorf("second delete should be idempotent: %v", err)
	}
}

func TestStatPayload(t *testing.T) {
	root := t.TempDir()
	payload := []byte("hello world")
	rel, _ := writePayload(context.Background(), root, "alice", "01STAT", payload)
	size, err := statPayload(root, rel)
	if err != nil {
		t.Fatalf("statPayload: %v", err)
	}
	if size != int64(len(payload)) {
		t.Errorf("size = %d, want %d", size, len(payload))
	}
}

func TestStatPayloadMissing(t *testing.T) {
	root := t.TempDir()
	_, err := statPayload(root, "nonexistent/file.bin")
	if err == nil {
		t.Error("expected error for missing file")
	}
}
