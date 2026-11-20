package appattesttest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"math/big"
	"testing"
	"time"
)

// ---- CBOR encoding ----

// Map is a CBOR map with text keys, in encoded order.
type Map []KV

// KV is one entry of a Map: the key (a string) and the value, KV{"fmt", "apple-appattest"}. An
// array, not a struct, so the short literal passes vet's composites check outside this package.
type KV [2]any

// Raw is appended to the encoding as it is (a float, a tag, a broken item).
type Raw []byte

// CBOR encodes v: uint64, int, []byte, string, []any, Map and Raw, definite lengths only. Any
// other type panics: a test's mistake.
func CBOR(v any) []byte { return cborAppend(nil, v) }

func cborHead(b []byte, major byte, n uint64) []byte {
	m := major << 5
	switch {
	case n < 24:
		return append(b, m|byte(n))
	case n <= 0xff:
		return append(b, m|24, byte(n))
	case n <= 0xffff:
		return binary.BigEndian.AppendUint16(append(b, m|25), uint16(n))
	case n <= 0xffffffff:
		return binary.BigEndian.AppendUint32(append(b, m|26), uint32(n))
	default:
		return binary.BigEndian.AppendUint64(append(b, m|27), n)
	}
}

func cborAppend(b []byte, v any) []byte {
	switch x := v.(type) {
	case uint64:
		return cborHead(b, 0, x)
	case int:
		if x >= 0 {
			return cborHead(b, 0, uint64(x))
		}
		return cborHead(b, 1, uint64(-1-x))
	case []byte:
		return append(cborHead(b, 2, uint64(len(x))), x...)
	case string:
		return append(cborHead(b, 3, uint64(len(x))), x...)
	case []any:
		b = cborHead(b, 4, uint64(len(x)))
		for _, it := range x {
			b = cborAppend(b, it)
		}
		return b
	case Map:
		b = cborHead(b, 5, uint64(len(x)))
		for _, p := range x {
			k, ok := p[0].(string)
			if !ok {
				panic("appattesttest.CBOR: a map key that is not a string")
			}
			b = cborAppend(cborAppend(b, k), p[1])
		}
		return b
	case Raw:
		return append(b, x...)
	default:
		panic("appattesttest.CBOR: unsupported type")
	}
}

// ---- certificates ----

// CA is a certificate and the key that signs below it.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// NewKey is a fresh ECDSA key on curve c.
func NewKey(t testing.TB, c elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// MustCert signs tmpl under parent with signer and parses the result.
func MustCert(t testing.TB, tmpl, parent *x509.Certificate, pub any, signer *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// NewSerial is a random positive serial number (no shared counter, so tests may run in parallel).
func NewSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		panic(err)
	}
	return n.Add(n, big.NewInt(1))
}

// CATemplate is a CA certificate's template, valid from a day before now for a year.
func CATemplate(name string, now time.Time) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:          NewSerial(),
		Subject:               pkix.Name{CommonName: name, Organization: []string{"Test"}},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
}

// NewRoot is a self-signed P-384 root, as Apple's is.
func NewRoot(t testing.TB, name string, now time.Time) CA {
	key := NewKey(t, elliptic.P384())
	tmpl := CATemplate(name, now)
	return CA{Cert: MustCert(t, tmpl, tmpl, &key.PublicKey, key), Key: key}
}

// IssueCA is an intermediate CA under p.
func (p CA) IssueCA(t testing.TB, name string, now time.Time) CA {
	key := NewKey(t, elliptic.P384())
	return CA{Cert: MustCert(t, CATemplate(name, now), p.Cert, &key.PublicKey, p.Key), Key: key}
}

// OIDNonce is Apple's extension holding the nonce in the credential certificate.
var OIDNonce = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 8, 2}

// NonceExtension is Apple's DER SEQUENCE { [1] EXPLICIT OCTET STRING nonce }.
func NonceExtension(t testing.TB, nonce []byte) []byte {
	t.Helper()
	octets, err := asn1.Marshal(nonce)
	if err != nil {
		t.Fatal(err)
	}
	tagged, err := asn1.Marshal(asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 1, IsCompound: true, Bytes: octets})
	if err != nil {
		t.Fatal(err)
	}
	seq, err := asn1.Marshal(asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSequence, IsCompound: true, Bytes: tagged})
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

// AAGUIDProduction is a production attestation's AAGUID: "appattest" and seven zero bytes.
func AAGUIDProduction() []byte { return append([]byte("appattest"), make([]byte, 7)...) }

// AuthData is an attestation's authenticator data: rpIdHash | flags | counter | aaguid |
// credIdLen | credId | a stand-in for the COSE key (no verifier reads it).
func AuthData(rpID string, flags byte, counter uint32, aaguid, credID []byte) []byte {
	h := sha256.Sum256([]byte(rpID))
	b := append(h[:], flags)
	b = binary.BigEndian.AppendUint32(b, counter)
	b = append(b, aaguid...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(credID)))
	b = append(b, credID...)
	return append(b, CBOR(Map{{"1", 2}, {"3", -7}})...)
}

// Concat is a fresh a ‖ b.
func Concat(a, b []byte) []byte {
	out := make([]byte, 0, len(a)+len(b))
	return append(append(out, a...), b...)
}

// Assertion is generateAssertion's CBOR: authenticatorData (rpID, counter) signed by key over
// SHA-256(authenticatorData ‖ cdh), the digest checked being SHA-256 of that nonce.
func Assertion(t testing.TB, key *ecdsa.PrivateKey, rpID string, counter uint32, cdh [32]byte) []byte {
	t.Helper()
	h := sha256.Sum256([]byte(rpID))
	ad := binary.BigEndian.AppendUint32(append(h[:], 0x01), counter)
	nonce := sha256.Sum256(Concat(ad, cdh[:]))
	digest := sha256.Sum256(nonce[:])
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return CBOR(Map{{"signature", sig}, {"authenticatorData", ad}})
}

// ---- one install ----

// App is one install's App Attest key under a generated chain (Root, then Inter) for AppID, at
// the time Now: what DCAppAttestService would produce, with test roots.
type App struct {
	AppID       string
	Now         time.Time
	Root, Inter CA
	Key         *ecdsa.PrivateKey // the install's key
	Point       []byte            // its 65-byte uncompressed point
	KeyID       [32]byte          // SHA-256 of Point
}

// NewApp generates a chain and a key for appID at now.
func NewApp(t testing.TB, appID string, now time.Time) *App {
	t.Helper()
	root := NewRoot(t, "Test App Attestation Root CA", now)
	a := &App{AppID: appID, Now: now, Root: root, Inter: root.IssueCA(t, "Test App Attestation CA 1", now),
		Key: NewKey(t, elliptic.P256())}
	var err error
	if a.Point, err = a.Key.PublicKey.Bytes(); err != nil {
		t.Fatal(err)
	}
	a.KeyID = sha256.Sum256(a.Point)
	return a
}

// KeyIDString is the key id as generateKey returns it: standard base64, 44 characters.
func (a *App) KeyIDString() string { return base64.StdEncoding.EncodeToString(a.KeyID[:]) }

// Attestation is a valid production attestation of the key for the challenge's clientDataHash cdh
// (counter 0, the App ID's RP ID hash, the credential id the key id).
func (a *App) Attestation(t testing.TB, cdh [32]byte) []byte {
	t.Helper()
	return a.AttestationWith(t, cdh, AAGUIDProduction())
}

// AttestationWith is Attestation with another AAGUID ("appattestdevelop" is a development one).
func (a *App) AttestationWith(t testing.TB, cdh [32]byte, aaguid []byte) []byte {
	t.Helper()
	ad := AuthData(a.AppID, 0x40, 0, aaguid, a.KeyID[:])
	nonce := sha256.Sum256(Concat(ad, cdh[:]))
	leaf := MustCert(t, &x509.Certificate{
		SerialNumber:    NewSerial(),
		Subject:         pkix.Name{CommonName: "test credential"},
		NotBefore:       a.Now.Add(-48 * time.Hour),
		NotAfter:        a.Now.Add(24 * time.Hour),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtraExtensions: []pkix.Extension{{Id: OIDNonce, Value: NonceExtension(t, nonce[:])}},
	}, a.Inter.Cert, &a.Key.PublicKey, a.Inter.Key)
	stmt := Map{{"x5c", []any{leaf.Raw, a.Inter.Cert.Raw}}, {"receipt", []byte("receipt bytes, unused")}}
	return CBOR(Map{{"fmt", "apple-appattest"}, {"attStmt", stmt}, {"authData", ad}})
}

// Assertion is a valid assertion of the key with counter over the challenge's clientDataHash cdh.
func (a *App) Assertion(t testing.TB, counter uint32, cdh [32]byte) []byte {
	t.Helper()
	return Assertion(t, a.Key, a.AppID, counter, cdh)
}

// RingLine is one line of APP_ATTEST_TOKEN_KEYS with the 8-hex-digit kid n and the 32 bytes first,
// first+1, ...: built at run time, never a key-shaped literal (gitleaks).
func RingLine(first byte, n uint32) string {
	k := make([]byte, 32)
	for i := range k {
		k[i] = first + byte(i)
	}
	return hex8(n) + ":" + base64.StdEncoding.EncodeToString(k)
}

func hex8(n uint32) string {
	const digits = "0123456789abcdef"
	var b [8]byte
	for i := 7; i >= 0; i-- {
		b[i] = digits[n&0xf]
		n >>= 4
	}
	return string(b[:])
}
