package sealed

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

func newKey(t *testing.T) *Key {
	t.Helper()
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	k, err := ParseKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	k := newKey(t)
	ct, err := k.Seal([]byte("r.refresh-token"), []byte("user-1"))
	if err != nil {
		t.Fatal(err)
	}
	if ct[0] != version || bytes.Contains(ct, []byte("refresh-token")) {
		t.Fatalf("ciphertext must be versioned and not contain the plaintext: %x", ct)
	}
	pt, err := k.Open(ct, []byte("user-1"))
	if err != nil || string(pt) != "r.refresh-token" {
		t.Fatalf("open: %q %v", pt, err)
	}
	again, _ := k.Seal([]byte("r.refresh-token"), []byte("user-1"))
	if bytes.Equal(ct, again) {
		t.Fatal("each seal must use a fresh nonce")
	}
}

func TestOpenRejectsAnotherRowKeyOrTampering(t *testing.T) {
	k := newKey(t)
	ct, _ := k.Seal([]byte("secret"), []byte("user-1"))
	if _, err := k.Open(ct, []byte("user-2")); !errors.Is(err, ErrOpen) {
		t.Error("a value copied to another user's row must not open")
	}
	if _, err := newKey(t).Open(ct, []byte("user-1")); !errors.Is(err, ErrOpen) {
		t.Error("another key must not open it")
	}
	bad := append([]byte{}, ct...)
	bad[len(bad)-1] ^= 1
	if _, err := k.Open(bad, []byte("user-1")); !errors.Is(err, ErrOpen) {
		t.Error("a tampered value must not open")
	}
	if _, err := k.Open([]byte{2, 0, 0}, nil); !errors.Is(err, ErrOpen) {
		t.Error("an unknown version must not open")
	}
}

func TestParseKeyNeeds32Bytes(t *testing.T) {
	for _, s := range []string{"", "not base64!", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := ParseKey(s); err == nil {
			t.Errorf("%q must be refused", s)
		}
	}
}
