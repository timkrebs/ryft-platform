package sealed

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// version prefixes every ciphertext, so the key can be rotated later.
const version byte = 1

// Key is an AES-256-GCM key. Build it with ParseKey: it checks the 32 bytes,
// and a zero Key{} has no cipher and panics on Seal and Open.
type Key struct{ aead cipher.AEAD }

// ParseKey reads a base64-encoded 32-byte key.
func ParseKey(b64 string) (*Key, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("data encryption key: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("data encryption key: %d bytes, want 32", len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Key{aead: aead}, nil
}

// Seal returns version || nonce || ciphertext.
func (k *Key) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{version}, nonce...)
	return k.aead.Seal(out, nonce, plaintext, aad), nil
}

// ErrOpen means the value was not sealed with this key and associated data.
var ErrOpen = errors.New("sealed: cannot open")

// Open reverses Seal. Every failure (short input, unknown version, wrong key or
// associated data) is the same ErrOpen, so a caller cannot tell a tampered
// value from a copied one.
func (k *Key) Open(sealed, aad []byte) ([]byte, error) {
	n := k.aead.NonceSize()
	if len(sealed) < 1+n || sealed[0] != version {
		return nil, ErrOpen
	}
	pt, err := k.aead.Open(nil, sealed[1:1+n], sealed[1+n:], aad)
	if err != nil {
		return nil, ErrOpen
	}
	return pt, nil
}
