package appattest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TokenTTL is an app token's lifetime (ttl_sec 3600); MinTokenTTL is the floor a
// test holds it to (AA3), so the app's renewal with 300 s left never loops.
const (
	TokenTTL    = time.Hour
	MinTokenTTL = 600 * time.Second
)

// The token format (D245): v1.<kid>.<body>.<mac>.
const (
	tokenVersion  = "v1"
	tokenMACLabel = "ryft-app-token/v1/" //nolint:gosec // the MAC's domain label (D245), not a credential
	maxTokenLen   = 512
	tokenSkew     = 60 * time.Second
	bodyLen       = 16 + 16 + 8 + 8 // user, SHA-256(key id)[:16], iat, exp
	kidLen        = 8
	ringKeyLen    = 32
	maxRingKeys   = 3
)

var b64url = base64.RawURLEncoding.Strict()

// Reason is the result of Check: Valid, or why a token is not.
type Reason string

// The reasons; all but Valid are the warn line's and the metric's values.
const (
	Valid        Reason = ""
	Missing      Reason = "missing"
	Invalid      Reason = "invalid"
	Expired      Reason = "expired"
	OtherAccount Reason = "other_account"
)

// Ring holds the token keys: the first signs, every one verifies.
type Ring struct {
	keys   map[string][]byte
	signer string
}

// ParseRing reads APP_ATTEST_TOKEN_KEYS: 1 to 3 non-empty lines
// "<kid>:<standard base64 of 32 bytes>", kid 8 lower-case hex digits and each
// kid once; one trailing newline is allowed. Errors name the line, never its
// content.
func ParseRing(s string) (*Ring, error) {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil, errors.New("appattest: the token key ring is empty")
	}
	lines := strings.Split(s, "\n")
	if len(lines) > maxRingKeys {
		return nil, fmt.Errorf("appattest: the token key ring has %d lines, at most %d", len(lines), maxRingKeys)
	}
	r := &Ring{keys: make(map[string][]byte, len(lines))}
	for i, line := range lines {
		kid, enc, ok := strings.Cut(line, ":")
		if !ok || !validKid(kid) {
			return nil, fmt.Errorf("appattest: token key ring line %d: not <8 lower-case hex digits>:<base64>", i+1)
		}
		key, ok := DecodeBlob(enc, keyIDLen)
		if !ok || len(key) != ringKeyLen {
			return nil, fmt.Errorf("appattest: token key ring line %d: the key is not standard base64 of %d bytes", i+1, ringKeyLen)
		}
		if _, dup := r.keys[kid]; dup {
			return nil, fmt.Errorf("appattest: token key ring line %d: a kid already named on an earlier line", i+1)
		}
		r.keys[kid] = key
		if i == 0 {
			r.signer = kid
		}
	}
	return r, nil
}

func validKid(s string) bool {
	if len(s) != kidLen {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Mint issues a token for user and keyID at now with the ring's first key; it
// expires TokenTTL later (whole seconds).
func (r *Ring) Mint(user uuid.UUID, keyID [32]byte, now time.Time) (token string, expires time.Time) {
	iat := now.Unix()
	exp := iat + int64(TokenTTL/time.Second)
	var body [bodyLen]byte
	copy(body[:16], user[:])
	kh := sha256.Sum256(keyID[:])
	copy(body[16:32], kh[:16])
	binary.BigEndian.PutUint64(body[32:40], uint64(iat))
	binary.BigEndian.PutUint64(body[40:48], uint64(exp))
	b := b64url.EncodeToString(body[:])
	mac := b64url.EncodeToString(r.mac(r.signer, b))
	return tokenVersion + "." + r.signer + "." + b + "." + mac, time.Unix(exp, 0).UTC()
}

// Check reads a token from X-RYFT-App-Token for the bearer's user at now,
// without a database: "" is Missing; over 512 characters, another version, an
// unknown kid, a bad MAC or an issue time more than 60 s ahead is Invalid; an
// expiry at or before now is Expired; another account's token is OtherAccount.
func (r *Ring) Check(token string, user uuid.UUID, now time.Time) Reason {
	if token == "" {
		return Missing
	}
	if len(token) > maxTokenLen {
		return Invalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != tokenVersion {
		return Invalid
	}
	kid, b, m := parts[1], parts[2], parts[3]
	if _, ok := r.keys[kid]; !ok {
		return Invalid
	}
	mac, ok := canonical(m, sha256.Size)
	if !ok || !hmac.Equal(mac, r.mac(kid, b)) {
		return Invalid
	}
	body, ok := canonical(b, bodyLen)
	if !ok {
		return Invalid
	}
	iat := int64(binary.BigEndian.Uint64(body[32:40]))
	exp := int64(binary.BigEndian.Uint64(body[40:48]))
	t := now.Unix()
	if iat > t+int64(tokenSkew/time.Second) {
		return Invalid
	}
	if exp <= t {
		return Expired
	}
	if !hmac.Equal(body[:16], user[:]) {
		return OtherAccount
	}
	return Valid
}

func (r *Ring) mac(kid, body string) []byte {
	h := hmac.New(sha256.New, r.keys[kid])
	h.Write([]byte(tokenMACLabel + kid + "." + body))
	return h.Sum(nil)
}

// canonical decodes base64url without padding of exactly n bytes, and only in
// its one encoding (the decoder skips line breaks; a token must not).
func canonical(s string, n int) ([]byte, bool) {
	if len(s) != b64url.EncodedLen(n) {
		return nil, false
	}
	b, err := b64url.DecodeString(s)
	if err != nil || len(b) != n {
		return nil, false
	}
	return b, true
}
