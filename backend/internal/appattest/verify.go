package appattest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	_ "embed" // Apple's root, apple_root.pem
	"encoding/asn1"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"time"
)

// EnvironmentProduction is the only environment this package accepts (D247):
// the AAGUID "appattest" followed by seven zero bytes.
const EnvironmentProduction = "production"

// ErrInvalid is the one error of Attestation and Assertion. It is wrapped with a
// fixed step name (Step), never with a byte of the input (AA4).
var ErrInvalid = errors.New("appattest: invalid")

// The steps, in the order API.md checks them; Step returns one of these.
const (
	stepCBOR         = "cbor"
	stepFmt          = "fmt"
	stepX5C          = "x5c"
	stepChain        = "chain"
	stepNonce        = "nonce"
	stepKeyID        = "key_id"
	stepCredentialID = "credential_id" //nolint:gosec // a step name, not a credential
	stepRPID         = "rp_id"
	stepCounter      = "counter"
	stepAAGUID       = "aaguid"
	stepSignature    = "signature"
)

type stepError struct{ step string }

func (e *stepError) Error() string { return "appattest: invalid (" + e.step + ")" }
func (e *stepError) Unwrap() error { return ErrInvalid }

func invalid(step string) error { return &stepError{step: step} }

// Step names the check an error of Attestation or Assertion failed (cbor, fmt,
// x5c, chain, nonce, key_id, credential_id, rp_id, counter, aaguid, signature),
// or "" for any other error.
func Step(err error) string {
	var se *stepError
	if errors.As(err, &se) {
		return se.step
	}
	return ""
}

//go:embed apple_root.pem
var appleRootPEM []byte

// oidNonce is Apple's extension holding the nonce in the credential certificate.
var oidNonce = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 8, 2}

// aaguidProduction is the AAGUID of a production attestation.
var aaguidProduction = [16]byte{'a', 'p', 'p', 'a', 't', 't', 'e', 's', 't'}

// The authenticator data layout: rpIdHash 32 | flags 1 | counter 4 (big-endian),
// then in an attestation the attested credential data: aaguid 16 | credential id
// length 2 (big-endian) | credential id | the credential's public key (unused).
const (
	authDataHeaderLen = 37
	authDataCredLen   = authDataHeaderLen + 16 + 2
	maxX5C            = 4
)

// Verifier checks attestations and assertions for one App ID against one root.
type Verifier struct {
	roots    *x509.CertPool
	rpIDHash [32]byte
	now      func() time.Time
}

// Attested is what a passing attestation yields.
type Attested struct {
	PublicKey   []byte // the 65-byte uncompressed P-256 point
	Environment string // EnvironmentProduction
}

// appleRoot returns Apple's App Attestation Root CA, embedded in the binary.
func appleRoot() (*x509.Certificate, error) {
	block, rest := pem.Decode(appleRootPEM)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("appattest: the embedded Apple root is not one PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("appattest: the embedded Apple root does not parse")
	}
	return cert, nil
}

// NewVerifier returns a Verifier for appID (team id, ".", bundle id) against
// Apple's pinned root, on the system clock.
func NewVerifier(appID string) (*Verifier, error) {
	if appID == "" {
		return nil, errors.New("appattest: empty App ID")
	}
	root, err := appleRoot()
	if err != nil {
		return nil, err
	}
	return NewVerifierWithRoot(appID, root, time.Now), nil
}

// NewVerifierWithRoot returns a Verifier against root on the clock now (nil:
// the system clock), for tests with generated chains.
func NewVerifierWithRoot(appID string, root *x509.Certificate, now func() time.Time) *Verifier {
	if now == nil {
		now = time.Now
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	return &Verifier{roots: pool, rpIDHash: sha256.Sum256([]byte(appID)), now: now}
}

// Attestation runs API.md's seven checks of POST /v1/app-attest/keys, in its
// order, on the CBOR att for keyID (decoded) and the challenge's clientDataHash.
// The receipt must be present and is not used.
func (v *Verifier) Attestation(att []byte, keyID, clientDataHash [32]byte) (Attested, error) {
	// 1. The shape.
	top, err := decodeCBOR(att)
	if err != nil || top.kind != cborMap {
		return Attested{}, invalid(stepCBOR)
	}
	format, okF := top.field("fmt")
	stmt, okS := top.field("attStmt")
	authData, okA := top.field("authData")
	if !okF || format.kind != cborText || !okS || stmt.kind != cborMap || !okA || authData.kind != cborBytes ||
		len(authData.bytes) < authDataCredLen {
		return Attested{}, invalid(stepCBOR)
	}
	if string(format.bytes) != "apple-appattest" {
		return Attested{}, invalid(stepFmt)
	}
	if receipt, ok := stmt.field("receipt"); !ok || receipt.kind != cborBytes {
		return Attested{}, invalid(stepCBOR)
	}
	x5c, ok := stmt.field("x5c")
	if !ok || x5c.kind != cborArray || len(x5c.items) < 2 || len(x5c.items) > maxX5C {
		return Attested{}, invalid(stepX5C)
	}
	certs := make([]*x509.Certificate, 0, len(x5c.items))
	for _, it := range x5c.items {
		if it.kind != cborBytes {
			return Attested{}, invalid(stepX5C)
		}
		c, err := x509.ParseCertificate(it.bytes)
		if err != nil {
			return Attested{}, invalid(stepX5C)
		}
		certs = append(certs, c)
	}
	leaf, ad := certs[0], authData.bytes

	// 2. The chain to the root.
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: v.roots, Intermediates: inter, CurrentTime: v.now(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return Attested{}, invalid(stepChain)
	}

	// 3. The nonce in the credential certificate.
	nonce := sha256.Sum256(concat(ad, clientDataHash[:]))
	if got, ok := certNonce(leaf); !ok || !bytes.Equal(got, nonce[:]) {
		return Attested{}, invalid(stepNonce)
	}

	// 4. The key id: the certificate's key, then the credential id.
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return Attested{}, invalid(stepKeyID)
	}
	point, err := pub.Bytes()
	if err != nil || len(point) != 65 || sha256.Sum256(point) != keyID {
		return Attested{}, invalid(stepKeyID)
	}
	credLen := int(binary.BigEndian.Uint16(ad[authDataHeaderLen+16 : authDataCredLen]))
	if credLen != len(keyID) || len(ad) < authDataCredLen+credLen ||
		!bytes.Equal(ad[authDataCredLen:authDataCredLen+credLen], keyID[:]) {
		return Attested{}, invalid(stepCredentialID)
	}

	// 5. The App ID.
	if !bytes.Equal(ad[:32], v.rpIDHash[:]) {
		return Attested{}, invalid(stepRPID)
	}
	// 6. A fresh key's counter.
	if binary.BigEndian.Uint32(ad[33:37]) != 0 {
		return Attested{}, invalid(stepCounter)
	}
	// 7. Production only (D247).
	if !bytes.Equal(ad[37:53], aaguidProduction[:]) {
		return Attested{}, invalid(stepAAGUID)
	}
	return Attested{PublicKey: point, Environment: EnvironmentProduction}, nil
}

// Assertion runs API.md's assertion checks of POST /v1/app-attest/token (steps
// 2 to 5) on the CBOR assertion with the key's stored publicKey (65 bytes), the
// challenge's clientDataHash and the last counter accepted for the key. It
// returns the assertion's counter, which the caller stores.
func (v *Verifier) Assertion(assertion, publicKey []byte, clientDataHash [32]byte, lastCounter uint32) (uint32, error) {
	top, err := decodeCBOR(assertion)
	if err != nil || top.kind != cborMap {
		return 0, invalid(stepCBOR)
	}
	sig, okS := top.field("signature")
	authData, okA := top.field("authenticatorData")
	if !okS || sig.kind != cborBytes || !okA || authData.kind != cborBytes || len(authData.bytes) < authDataHeaderLen {
		return 0, invalid(stepCBOR)
	}
	ad := authData.bytes
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), publicKey)
	if err != nil {
		return 0, invalid(stepSignature)
	}
	nonce := sha256.Sum256(concat(ad, clientDataHash[:]))
	digest := sha256.Sum256(nonce[:])
	if !ecdsa.VerifyASN1(pub, digest[:], sig.bytes) {
		return 0, invalid(stepSignature)
	}
	if !bytes.Equal(ad[:32], v.rpIDHash[:]) {
		return 0, invalid(stepRPID)
	}
	counter := binary.BigEndian.Uint32(ad[33:37])
	if counter <= lastCounter {
		return 0, invalid(stepCounter)
	}
	return counter, nil
}

// certNonce returns the octet string of Apple's nonce extension, read
// tolerantly as SEQUENCE { ... [1] EXPLICIT OCTET STRING ... }.
func certNonce(c *x509.Certificate) ([]byte, bool) {
	for _, ext := range c.Extensions {
		if ext.Id.Equal(oidNonce) {
			return parseNonce(ext.Value)
		}
	}
	return nil, false
}

func parseNonce(der []byte) ([]byte, bool) {
	var seq asn1.RawValue
	rest, err := asn1.Unmarshal(der, &seq)
	if err != nil || len(rest) != 0 || seq.Class != asn1.ClassUniversal || seq.Tag != asn1.TagSequence || !seq.IsCompound {
		return nil, false
	}
	for inner := seq.Bytes; len(inner) > 0; {
		var el asn1.RawValue
		if inner, err = asn1.Unmarshal(inner, &el); err != nil {
			return nil, false
		}
		if el.Class != asn1.ClassContextSpecific || el.Tag != 1 || !el.IsCompound {
			continue
		}
		var octets []byte
		r, err := asn1.Unmarshal(el.Bytes, &octets)
		if err != nil || len(r) != 0 || len(octets) != sha256.Size {
			return nil, false
		}
		return octets, true
	}
	return nil, false
}

func concat(a, b []byte) []byte {
	out := make([]byte, 0, len(a)+len(b))
	return append(append(out, a...), b...)
}
