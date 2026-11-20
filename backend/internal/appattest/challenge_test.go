package appattest

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestNewChallenge(t *testing.T) {
	seen := map[string]bool{}
	for range 64 {
		c, err := NewChallenge()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != 43 {
			t.Fatalf("length %d, want 43", len(c))
		}
		b, err := base64.RawURLEncoding.Strict().DecodeString(c)
		if err != nil || len(b) != 32 {
			t.Fatalf("not base64url of 32 bytes: %v", err)
		}
		if seen[c] {
			t.Fatal("a challenge repeated")
		}
		seen[c] = true
	}
}

func TestClientDataHash(t *testing.T) {
	c := "q3Jx0mZ9cT1vY8kL2pW5nB7dF4hS6aE0uR3iO9yG1tM" // API.md's example
	if ClientDataHash(c) != sha256.Sum256([]byte(c)) {
		t.Fatal("not SHA-256 of the UTF-8 bytes")
	}
	if ClientDataHash(c) == ClientDataHash(c+"\n") || ClientDataHash(c) == ClientDataHash(`"`+c+`"`) {
		t.Fatal("a newline or quotes do not change the hash")
	}
}

func TestParseKeyID(t *testing.T) {
	raw := bytes.Repeat([]byte{0xfb, 0xff, 0xbf}, 11)[:32] // + and / in standard base64
	std := base64.StdEncoding.EncodeToString(raw)
	if !strings.ContainsAny(std, "+/") || len(std) != 44 {
		t.Fatalf("the vector %q does not use + or /", std)
	}
	got, ok := ParseKeyID(std)
	if !ok || !bytes.Equal(got[:], raw) {
		t.Fatalf("valid key id refused: %v", ok)
	}
	// API.md's example key id parses.
	if _, ok := ParseKeyID("pKqW2rZ8yH3nT6v+1mX9cF4sD7gJ0eL5uA2oI8w/3kE="); !ok {
		t.Fatal("API.md's example refused")
	}
	stray := std[:42] + "F=" // the last character carries bits the 32 bytes do not have
	refused := map[string]string{
		"empty":         "",
		"base64url":     base64.URLEncoding.EncodeToString(raw),
		"unpadded":      strings.TrimRight(std, "="),
		"33 bytes":      base64.StdEncoding.EncodeToString(append(raw, 0)),
		"31 bytes":      base64.StdEncoding.EncodeToString(raw[:31]),
		"line break":    std[:20] + "\n" + std[20:43],
		"stray bits":    stray,
		"space":         " " + std[:43],
		"48 characters": base64.StdEncoding.EncodeToString(make([]byte, 36)),
	}
	for name, s := range refused {
		if _, ok := ParseKeyID(s); ok {
			t.Errorf("%s: %q accepted", name, s)
		}
	}
}

func TestDecodeBlob(t *testing.T) {
	data := []byte("some CBOR \xfb\xff!") // 13 bytes: two padding characters
	s := base64.StdEncoding.EncodeToString(data)
	if !strings.HasSuffix(s, "==") || !strings.ContainsAny(s, "+/") {
		t.Fatalf("the vector %q has no padding or no + or /", s)
	}
	if b, ok := DecodeBlob(s, len(s)); !ok || !bytes.Equal(b, data) {
		t.Fatal("valid blob refused")
	}
	refused := map[string]string{
		"over the limit": s + "AAAA",
		"empty":          "",
		"base64url":      base64.URLEncoding.EncodeToString(data),
		"unpadded":       strings.TrimRight(s, "="),
		"line break":     s[:4] + "\n" + s[4:],
		"CR":             s[:4] + "\r" + s[4:],
		"line wrapped":   s + "\r\n",
		"not base64":     "!!!!",
	}
	for name, in := range refused {
		if _, ok := DecodeBlob(in, len(s)); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}
