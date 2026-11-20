package appattest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/appattest/appattesttest"
)

// Shared builders: the generated CA chain, the CBOR encoder and the assertion live in
// appattesttest (the handler tests of cmd/api use them too); this file adds the attestation
// spec with a knob per refusal step.

const testAppID = "TEAMID1234.dev.example.app"

var testNow = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

// ---- CBOR encoding (test only) ----

type (
	cmap    = appattesttest.Map // a map in encoded order
	ckv     = appattesttest.KV
	rawCBOR = appattesttest.Raw // appended as is
)

func cborEncode(v any) []byte { return appattesttest.CBOR(v) }

// ---- certificates ----

type ca struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func fromCA(c appattesttest.CA) ca { return ca{cert: c.Cert, key: c.Key} }

func newKey(t testing.TB, c elliptic.Curve) *ecdsa.PrivateKey { return appattesttest.NewKey(t, c) }

// newRoot is a self-signed P-384 root, as Apple's is.
func newRoot(t testing.TB, name string) ca { return fromCA(appattesttest.NewRoot(t, name, testNow)) }

func (p ca) issueCA(t testing.TB, name string) ca {
	return fromCA(appattesttest.CA{Cert: p.cert, Key: p.key}.IssueCA(t, name, testNow))
}

// nonceExtension is Apple's DER SEQUENCE { [1] EXPLICIT OCTET STRING nonce }.
func nonceExtension(t testing.TB, nonce []byte) []byte { return appattesttest.NonceExtension(t, nonce) }

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
	app := appattesttest.NewApp(t, testAppID, testNow)
	f := &fixture{root: fromCA(app.Root), inter: fromCA(app.Inter), key: app.Key, point: app.Point, keyID: app.KeyID}
	var err error
	if f.challenge, err = NewChallenge(); err != nil {
		t.Fatal(err)
	}
	f.cdh = ClientDataHash(f.challenge)
	f.verifier = NewVerifierWithRoot(testAppID, f.root.cert, func() time.Time { return testNow })
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
		aaguid:       appattesttest.AAGUIDProduction(),
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
	return appattesttest.AuthData(rpID, flags, counter, aaguid, credID)
}

func (f *fixture) build(t testing.TB, s attSpec) []byte {
	t.Helper()
	ad := authData(s.rpID, 0x40, s.counter, s.aaguid, s.credID)
	nonce := sha256.Sum256(concat(ad, s.nonceCDH[:]))
	leafTmpl := &x509.Certificate{
		SerialNumber: appattesttest.NewSerial(),
		Subject:      pkix.Name{CommonName: "test credential"},
		NotBefore:    testNow.Add(-48 * time.Hour),
		NotAfter:     s.leafNotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if !s.noNonce {
		leafTmpl.ExtraExtensions = []pkix.Extension{{Id: oidNonce, Value: nonceExtension(t, nonce[:])}}
	}
	leaf := appattesttest.MustCert(t, leafTmpl, s.issuer.cert, s.leafPub, s.issuer.key)
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
	return appattesttest.Assertion(t, key, rpID, counter, cdh)
}

// The helper package's OID is Apple's, the one the verifier reads.
func TestHelperOIDIsTheVerifiers(t *testing.T) {
	if !appattesttest.OIDNonce.Equal(oidNonce) || string(appattesttest.AAGUIDProduction()) != string(aaguidProduction[:]) {
		t.Fatal("appattesttest's nonce OID or production AAGUID is not the verifier's")
	}
}
