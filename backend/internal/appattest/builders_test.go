package appattest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"math/big"
	"testing"
	"time"
)

// Shared builders: a generated CA chain, App Attest attestations and assertions
// in CBOR, and a small test-only CBOR encoder.

const testAppID = "TEAMID1234.dev.example.app"

var testNow = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

// ---- CBOR encoding (test only) ----

type cmap []ckv // a map in encoded order

type ckv struct {
	k string
	v any
}

type rawCBOR []byte // appended as is

func cborEncode(v any) []byte { return cborAppend(nil, v) }

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
	case cmap:
		b = cborHead(b, 5, uint64(len(x)))
		for _, p := range x {
			b = cborAppend(cborAppend(b, p.k), p.v)
		}
		return b
	case rawCBOR:
		return append(b, x...)
	default:
		panic("cborAppend: unsupported type")
	}
}

// ---- certificates ----

type ca struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newKey(t testing.TB, c elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func mustCert(t testing.TB, tmpl, parent *x509.Certificate, pub any, signer *ecdsa.PrivateKey) *x509.Certificate {
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

// newSerial is a random positive serial number (no shared counter, so tests
// may run in parallel).
func newSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		panic(err)
	}
	return n.Add(n, big.NewInt(1))
}

func caTemplate(name string) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:          newSerial(),
		Subject:               pkix.Name{CommonName: name, Organization: []string{"Test"}},
		NotBefore:             testNow.Add(-24 * time.Hour),
		NotAfter:              testNow.Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
}

// newRoot is a self-signed P-384 root, as Apple's is.
func newRoot(t testing.TB, name string) ca {
	key := newKey(t, elliptic.P384())
	tmpl := caTemplate(name)
	return ca{cert: mustCert(t, tmpl, tmpl, &key.PublicKey, key), key: key}
}

func (p ca) issueCA(t testing.TB, name string) ca {
	key := newKey(t, elliptic.P384())
	return ca{cert: mustCert(t, caTemplate(name), p.cert, &key.PublicKey, p.key), key: key}
}

// nonceExtension is Apple's DER SEQUENCE { [1] EXPLICIT OCTET STRING nonce }.
func nonceExtension(t testing.TB, nonce []byte) []byte {
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

// ---- the fixture ----

type fixture struct {
	root, inter ca
	key         *ecdsa.PrivateKey // the app's key
	point       []byte            // its 65-byte uncompressed point
	keyID       [32]byte
	challenge   string
	cdh         [32]byte
	verifier    *Verifier
}

func newFixture(t testing.TB) *fixture {
	t.Helper()
	root := newRoot(t, "Test App Attestation Root CA")
	f := &fixture{root: root, inter: root.issueCA(t, "Test App Attestation CA 1")}
	f.key = newKey(t, elliptic.P256())
	var err error
	if f.point, err = f.key.PublicKey.Bytes(); err != nil {
		t.Fatal(err)
	}
	f.keyID = sha256.Sum256(f.point)
	if f.challenge, err = NewChallenge(); err != nil {
		t.Fatal(err)
	}
	f.cdh = ClientDataHash(f.challenge)
	f.verifier = NewVerifierWithRoot(testAppID, root.cert, func() time.Time { return testNow })
	return f
}

// attSpec describes one attestation; defaults() is a valid one.
type attSpec struct {
	fmt          string
	rpID         string
	counter      uint32
	aaguid       []byte
	credID       []byte
	nonceCDH     [32]byte // the clientDataHash the leaf's nonce is computed with
	noNonce      bool     // leave the extension out
	leafPub      any      // the leaf's public key
	leafNotAfter time.Time
	issuer       ca // signs the leaf
	extraCerts   int
	singleCert   bool
	noReceipt    bool
	trailing     []byte
}

func (f *fixture) defaults() attSpec {
	return attSpec{
		fmt:          "apple-appattest",
		rpID:         testAppID,
		aaguid:       append([]byte("appattest"), make([]byte, 7)...),
		credID:       f.keyID[:],
		nonceCDH:     f.cdh,
		leafPub:      &f.key.PublicKey,
		leafNotAfter: testNow.Add(24 * time.Hour),
		issuer:       f.inter,
	}
}

// authData is rpIdHash | flags | counter | aaguid | credIdLen | credId | a
// stand-in for the COSE key (the verifier never reads it).
func authData(rpID string, flags byte, counter uint32, aaguid, credID []byte) []byte {
	h := sha256.Sum256([]byte(rpID))
	b := append(h[:], flags)
	b = binary.BigEndian.AppendUint32(b, counter)
	b = append(b, aaguid...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(credID)))
	b = append(b, credID...)
	return append(b, cborEncode(cmap{{"1", 2}, {"3", -7}})...)
}

func (f *fixture) build(t testing.TB, s attSpec) []byte {
	t.Helper()
	ad := authData(s.rpID, 0x40, s.counter, s.aaguid, s.credID)
	nonce := sha256.Sum256(concat(ad, s.nonceCDH[:]))
	leafTmpl := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject:      pkix.Name{CommonName: "test credential"},
		NotBefore:    testNow.Add(-48 * time.Hour),
		NotAfter:     s.leafNotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if !s.noNonce {
		leafTmpl.ExtraExtensions = []pkix.Extension{{Id: oidNonce, Value: nonceExtension(t, nonce[:])}}
	}
	leaf := mustCert(t, leafTmpl, s.issuer.cert, s.leafPub, s.issuer.key)
	x5c := []any{leaf.Raw}
	if !s.singleCert {
		x5c = append(x5c, s.issuer.cert.Raw)
	}
	for range s.extraCerts {
		x5c = append(x5c, f.root.cert.Raw)
	}
	stmt := cmap{{"x5c", x5c}}
	if !s.noReceipt {
		stmt = append(stmt, ckv{"receipt", []byte("receipt bytes, unused")})
	}
	att := cborEncode(cmap{{"fmt", s.fmt}, {"attStmt", stmt}, {"authData", ad}})
	return append(att, s.trailing...)
}

// assertion signs authenticatorData (rpID, counter) with clientDataHash cdh.
func (f *fixture) assertion(t testing.TB, key *ecdsa.PrivateKey, rpID string, counter uint32, cdh [32]byte) []byte {
	t.Helper()
	h := sha256.Sum256([]byte(rpID))
	ad := binary.BigEndian.AppendUint32(append(h[:], 0x01), counter)
	nonce := sha256.Sum256(concat(ad, cdh[:]))
	digest := sha256.Sum256(nonce[:])
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return cborEncode(cmap{{"signature", sig}, {"authenticatorData", ad}})
}
