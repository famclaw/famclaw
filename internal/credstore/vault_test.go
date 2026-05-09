package credstore_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"

	"github.com/famclaw/famclaw/internal/credstore"
)

// testMachineID is a literal — tests must NOT call MachineID() because that
// would couple tests to the host environment.
const testMachineID = "test-machine-id-deadbeef"

func TestVault_RoundTrip(t *testing.T) {
	v, err := credstore.New(testMachineID)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	random1KB := make([]byte, 1024)
	if _, err := io.ReadFull(rand.Reader, random1KB); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	cases := []struct {
		name string
		pt   []byte
	}{
		{name: "nil", pt: nil},
		{name: "empty", pt: []byte{}},
		{name: "hello", pt: []byte("hello")},
		{name: "1KB random", pt: random1KB},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob, err := v.Encrypt(tc.pt)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}
			got, err := v.Decrypt(blob)
			if err != nil {
				t.Fatalf("Decrypt: %v", err)
			}
			// bytes.Equal treats nil and []byte{} as equal, which is what
			// we want here — the AEAD round-trip preserves length, not
			// the distinction between nil and empty slices.
			if !bytes.Equal(got, tc.pt) {
				t.Fatalf("round-trip mismatch: got %x want %x", got, tc.pt)
			}
		})
	}
}

func TestVault_TamperDetection(t *testing.T) {
	v, err := credstore.New(testMachineID)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	original, err := v.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	cases := []struct {
		name string
		idx  int // byte index to flip
	}{
		{name: "flip last byte (GCM tag)", idx: len(original) - 1},
		{name: "flip ciphertext body byte", idx: 12},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tampered := make([]byte, len(original))
			copy(tampered, original)
			tampered[tc.idx] ^= 0x01
			_, err := v.Decrypt(tampered)
			if err == nil {
				t.Fatalf("Decrypt of tampered blob: want error, got nil")
			}
			if !errors.Is(err, credstore.ErrMachineMismatch) {
				t.Fatalf("Decrypt error: want ErrMachineMismatch, got %v", err)
			}
		})
	}
}

func TestVault_TooShortCiphertext(t *testing.T) {
	v, err := credstore.New(testMachineID)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = v.Decrypt([]byte{1, 2, 3})
	if err == nil {
		t.Fatal("Decrypt of too-short blob: want error, got nil")
	}
	// Too-short input is a corruption / API misuse, not a key mismatch —
	// callers must not see ErrMachineMismatch here.
	if errors.Is(err, credstore.ErrMachineMismatch) {
		t.Fatalf("Decrypt of too-short blob: must NOT be ErrMachineMismatch, got %v", err)
	}
}

func TestVault_NonceUniqueness(t *testing.T) {
	v, err := credstore.New(testMachineID)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const iterations = 1000
	seen := make(map[[12]byte]struct{}, iterations)
	for i := 0; i < iterations; i++ {
		blob, err := v.Encrypt([]byte("same plaintext"))
		if err != nil {
			t.Fatalf("Encrypt[%d]: %v", i, err)
		}
		var key [12]byte
		copy(key[:], blob[:12])
		seen[key] = struct{}{}
	}
	if len(seen) != iterations {
		t.Fatalf("nonce uniqueness: got %d unique nonces over %d encryptions, want %d",
			len(seen), iterations, iterations)
	}
}

func TestVault_ErrMachineMismatch(t *testing.T) {
	v1, err := credstore.New("machine-A")
	if err != nil {
		t.Fatalf("New v1: %v", err)
	}
	v2, err := credstore.New("machine-B")
	if err != nil {
		t.Fatalf("New v2: %v", err)
	}
	blob, err := v1.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	_, err = v2.Decrypt(blob)
	if err == nil {
		t.Fatal("Decrypt with foreign vault: want error, got nil")
	}
	if !errors.Is(err, credstore.ErrMachineMismatch) {
		t.Fatalf("Decrypt with foreign vault: want ErrMachineMismatch, got %v", err)
	}
}

func TestVault_NewRejectsEmpty(t *testing.T) {
	v, err := credstore.New("")
	if err == nil {
		t.Fatal("New(\"\"): want error, got nil")
	}
	if v != nil {
		t.Fatalf("New(\"\"): want nil Vault on error, got %+v", v)
	}
}
