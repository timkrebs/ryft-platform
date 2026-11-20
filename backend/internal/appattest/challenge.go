package appattest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"
)

// ChallengeTTL is how long a challenge stays usable (API.md: 5 minutes).
const ChallengeTTL = 5 * time.Minute

// keyIDLen is the length of a key id in standard base64: 32 bytes, padded.
const keyIDLen = 44

// NewChallenge returns 32 random bytes in base64url without padding (43
// characters). The app treats it as opaque.
func NewChallenge() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// ClientDataHash is SHA-256 of the challenge string's UTF-8 bytes: what the app
// passes to attestKey and generateAssertion, and what the store keeps.
func ClientDataHash(challenge string) [32]byte {
	return sha256.Sum256([]byte(challenge))
}

// ParseKeyID decodes a key id as generateKey returns it: standard base64 with
// padding, exactly 44 characters, 32 bytes.
func ParseKeyID(s string) ([32]byte, bool) {
	var id [32]byte
	if len(s) != keyIDLen {
		return id, false
	}
	b, ok := DecodeBlob(s, keyIDLen)
	if !ok || len(b) != len(id) {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

// DecodeBlob decodes strict standard base64 (padding, no line breaks, no stray
// bits) of at most limit characters. An empty string is refused.
func DecodeBlob(s string, limit int) ([]byte, bool) {
	if s == "" || len(s) > limit || strings.ContainsAny(s, "\r\n") {
		return nil, false
	}
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, false
	}
	return b, true
}
