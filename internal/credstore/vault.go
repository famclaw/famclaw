// Package credstore provides a machine-bound credential vault using
// AES-256-GCM with a key derived from a stable per-host machine identifier
// via HKDF-SHA256.
//
// The Vault encrypts and decrypts opaque blobs. The encryption key is bound
// to the host's machine ID, so a vault encrypted on one machine cannot be
// decrypted on another — Decrypt returns ErrMachineMismatch in that case.
package credstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// saltConst is the HKDF salt. MUST remain "famclaw-cred-v1" — changing this
// value rotates the derived key and renders existing vault blobs unreadable.
var saltConst = []byte("famclaw-cred-v1")

// infoConst is the HKDF info parameter. MUST remain lowercase "vault" —
// changing this value rotates the derived key.
var infoConst = []byte("vault")

const (
	keyLen   = 32 // AES-256
	nonceLen = 12 // GCM standard nonce size
	tagLen   = 16 // GCM authentication tag size
)

// ErrMachineMismatch is returned by Decrypt when an authenticated decryption
// fails on a structurally valid ciphertext. This usually means the blob was
// produced on a different machine (different machine ID → different key) or
// has been tampered with. Callers should compare with errors.Is.
//
// This sentinel MUST NOT be wrapped — callers depend on equality.
var ErrMachineMismatch = errors.New("machine id mismatch")

// Vault holds an AES-GCM AEAD bound to a particular machine ID.
type Vault struct {
	aead cipher.AEAD
}

// New derives an AES-256 key from the supplied machine ID using
// HKDF-SHA256(salt=saltConst, info=infoConst) and returns a Vault ready to
// Encrypt/Decrypt. The machineID must be non-empty; tests pass a literal
// string here rather than calling MachineID().
func New(machineID string) (*Vault, error) {
	if machineID == "" {
		return nil, errors.New("credstore: empty machine id")
	}
	r := hkdf.New(sha256.New, []byte(machineID), saltConst, infoConst)
	key := make([]byte, keyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("credstore: deriving key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("credstore: creating cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("credstore: creating GCM: %w", err)
	}
	return &Vault{aead: aead}, nil
}

// Encrypt seals plaintext under the vault's key with a fresh random 12-byte
// nonce drawn from crypto/rand. The nonce is prepended to the GCM output:
//
//	result = nonce (12B) || ciphertext || tag (16B)
//
// A new nonce is generated for every call — never reused, never derived.
func (v *Vault) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("credstore: reading nonce: %w", err)
	}
	// Pass nil as dst so Seal allocates a fresh slice for the ciphertext;
	// we then append it to the nonce. Do NOT reuse the nonce slice as dst.
	ct := v.aead.Seal(nil, nonce, plaintext, nil)
	return append(nonce, ct...), nil
}

// Decrypt opens a blob produced by Encrypt and returns the plaintext.
//
// If the blob is shorter than nonceLen+tagLen (28 bytes) it is structurally
// invalid and Decrypt returns a generic "ciphertext too short" error — NOT
// ErrMachineMismatch, because the issue is corruption, not key mismatch.
//
// If the GCM authentication tag fails to verify on a structurally valid
// blob, Decrypt returns ErrMachineMismatch as a bare sentinel (not wrapped),
// so callers can detect machine-binding failures with errors.Is.
func (v *Vault) Decrypt(blob []byte) ([]byte, error) {
	if len(blob) < nonceLen+tagLen {
		return nil, fmt.Errorf("credstore: ciphertext too short")
	}
	nonce, ct := blob[:nonceLen], blob[nonceLen:]
	pt, err := v.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrMachineMismatch
	}
	return pt, nil
}
