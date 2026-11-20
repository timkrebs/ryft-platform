package appattest

import (
	"bytes"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type attCase struct {
	name  string
	att   []byte
	keyID [32]byte
	cdh   [32]byte
	step  string // "" passes
}

// attestationCases is the happy path and one refusal (or more) per step.
func attestationCases(t testing.TB, f *fixture) []attCase {
	t.Helper()
	spec := func(mut func(*attSpec)) []byte {
		s := f.defaults()
		if mut != nil {
			mut(&s)
		}
		return f.build(t, s)
	}
	otherRoot := newRoot(t, "Another Root")
	otherKey := newKey(t, elliptic.P256())
	var otherID [32]byte
	copy(otherID[:], bytes.Repeat([]byte{0x5a}, 32))
	leafOnly := spec(nil) // a valid attestation to take the leaf from
	top, err := decodeCBOR(leafOnly)
	if err != nil {
		t.Fatal(err)
	}
	stmt, _ := top.field("attStmt")
	x5c, _ := stmt.field("x5c")
	leaf := x5c.items[0].bytes
	ad, _ := top.field("authData")
	receipt := ckv{"receipt", []byte{1}}
	custom := func(stmt cmap, fields ...ckv) []byte {
		m := cmap{{"fmt", "apple-appattest"}, {"attStmt", stmt}, {"authData", ad.bytes}}
		return cborEncode(append(m, fields...))
	}

	c := func(name string, att []byte, step string) attCase {
		return attCase{name: name, att: att, keyID: f.keyID, cdh: f.cdh, step: step}
	}
	cases := []attCase{
		c("happy path", spec(nil), ""),
		c("unknown extra top-level key tolerated", custom(cmap{{"x5c", []any{leaf, f.inter.cert.Raw}}, receipt}, ckv{"extra", 1}), ""),

		// 1. the shape
		c("not a map", cborEncode([]any{1, 2}), stepCBOR),
		c("not CBOR", []byte("{\"fmt\":\"apple-appattest\"}"), stepCBOR),
		c("empty", nil, stepCBOR),
		c("trailing CBOR bytes", spec(func(s *attSpec) { s.trailing = []byte{0x00} }), stepCBOR),
		c("missing authData", cborEncode(cmap{{"fmt", "apple-appattest"}, {"attStmt", cmap{}}}), stepCBOR),
		c("fmt not text", cborEncode(cmap{{"fmt", []byte("apple-appattest")}, {"attStmt", cmap{}}, {"authData", ad.bytes}}), stepCBOR),
		c("attStmt not a map", cborEncode(cmap{{"fmt", "apple-appattest"}, {"attStmt", []any{}}, {"authData", ad.bytes}}), stepCBOR),
		c("authData too short", cborEncode(cmap{{"fmt", "apple-appattest"}, {"attStmt", cmap{}}, {"authData", ad.bytes[:54]}}), stepCBOR),
		c("no receipt", spec(func(s *attSpec) { s.noReceipt = true }), stepCBOR),
		c("wrong fmt", spec(func(s *attSpec) { s.fmt = "packed" }), stepFmt),
		c("wrong fmt before an expired leaf", spec(func(s *attSpec) { s.fmt = "none"; s.leafNotAfter = testNow.Add(-time.Minute) }), stepFmt),
		c("x5c with one certificate", spec(func(s *attSpec) { s.singleCert = true }), stepX5C),
		c("x5c with five certificates", spec(func(s *attSpec) { s.extraCerts = 3 }), stepX5C),
		c("x5c missing", custom(cmap{receipt}), stepX5C),
		c("x5c not an array", custom(cmap{{"x5c", leaf}, receipt}), stepX5C),
		c("x5c item not bytes", custom(cmap{{"x5c", []any{leaf, "intermediate"}}, receipt}), stepX5C),
		c("x5c item not a certificate", custom(cmap{{"x5c", []any{leaf, []byte{0x30, 0x03, 0x02, 0x01, 0x01}}}, receipt}), stepX5C),

		// 2. the chain
		c("chain to another root", spec(func(s *attSpec) { s.issuer = otherRoot.issueCA(t, "Other CA") }), stepChain),
		c("expired leaf", spec(func(s *attSpec) { s.leafNotAfter = testNow.Add(-time.Minute) }), stepChain),
		c("leaf of another chain", spec(func(s *attSpec) { s.issuer = otherRoot }), stepChain),

		// 3. the nonce
		c("nonce over another challenge", spec(func(s *attSpec) { s.nonceCDH = ClientDataHash("another") }), stepNonce),
		c("no nonce extension", spec(func(s *attSpec) { s.noNonce = true }), stepNonce),
		c("the intermediate first", custom(cmap{{"x5c", []any{f.inter.cert.Raw, leaf}}, receipt}), stepNonce),
		{name: "verified with another challenge", att: spec(nil), keyID: f.keyID, cdh: ClientDataHash("another"), step: stepNonce},

		// 4. the key id
		{name: "wrong key id", att: spec(nil), keyID: otherID, cdh: f.cdh, step: stepKeyID},
		c("leaf holds another key", spec(func(s *attSpec) { s.leafPub = &otherKey.PublicKey }), stepKeyID),
		c("leaf key on P-384", spec(func(s *attSpec) { s.leafPub = &newKey(t, elliptic.P384()).PublicKey }), stepKeyID),
		c("credential id mismatch", spec(func(s *attSpec) { s.credID = otherID[:] }), stepCredentialID),
		c("credential id of 31 bytes", spec(func(s *attSpec) { s.credID = f.keyID[:31] }), stepCredentialID),

		// 5.-7.
		c("rp id of another app", spec(func(s *attSpec) { s.rpID = "TEAMID1234.dev.example.other" }), stepRPID),
		c("rp id before the counter", spec(func(s *attSpec) { s.rpID = "x"; s.counter = 1 }), stepRPID),
		c("counter not 0", spec(func(s *attSpec) { s.counter = 1 }), stepCounter),
		c("development AAGUID", spec(func(s *attSpec) { s.aaguid = []byte("appattestdevelop") }), stepAAGUID),
		c("zero AAGUID", spec(func(s *attSpec) { s.aaguid = make([]byte, 16) }), stepAAGUID),
	}
	return cases
}

func TestAttestation(t *testing.T) {
	f := newFixture(t)
	for _, tc := range attestationCases(t, f) {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.verifier.Attestation(tc.att, tc.keyID, tc.cdh)
			if tc.step == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if !bytes.Equal(got.PublicKey, f.point) || got.Environment != EnvironmentProduction {
					t.Fatalf("got %x %q, want the app's point and production", got.PublicKey, got.Environment)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) || Step(err) != tc.step {
				t.Fatalf("err %v (step %q), want step %q", err, Step(err), tc.step)
			}
			if got.PublicKey != nil || got.Environment != "" {
				t.Fatalf("a refusal returned %+v", got)
			}
		})
	}
}

func TestAttestationClock(t *testing.T) {
	f := newFixture(t)
	att := f.build(t, f.defaults())
	late := NewVerifierWithRoot(testAppID, f.root.cert, func() time.Time { return testNow.Add(48 * time.Hour) })
	if _, err := late.Attestation(att, f.keyID, f.cdh); Step(err) != stepChain {
		t.Fatalf("after the leaf's NotAfter: %v, want chain", err)
	}
	if v := NewVerifierWithRoot(testAppID, f.root.cert, nil); v.now == nil {
		t.Fatal("nil clock not replaced")
	}
}

func TestNewVerifier(t *testing.T) {
	v, err := NewVerifier(testAppID)
	if err != nil {
		t.Fatal(err)
	}
	if v.rpIDHash != sha256.Sum256([]byte(testAppID)) {
		t.Fatal("rp id hash is not SHA-256 of the App ID")
	}
	// Apple's root refuses a generated chain.
	f := newFixture(t)
	if _, err := v.Attestation(f.build(t, f.defaults()), f.keyID, f.cdh); Step(err) != stepChain {
		t.Fatalf("generated chain against Apple's root: %v, want chain", err)
	}
	if _, err := NewVerifier(""); err == nil {
		t.Fatal("empty App ID accepted")
	}
}

func TestParseNonce(t *testing.T) {
	nonce := bytes.Repeat([]byte{7}, 32)
	mustDER := func(v any) []byte {
		b, err := asn1.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	seq := func(parts ...[]byte) []byte {
		return mustDER(asn1.RawValue{Tag: asn1.TagSequence, IsCompound: true, Bytes: bytes.Join(parts, nil)})
	}
	tagged := func(tag int, compound bool, inner []byte) []byte {
		return mustDER(asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: tag, IsCompound: compound, Bytes: inner})
	}
	octets := mustDER(nonce)
	cases := []struct {
		name string
		der  []byte
		ok   bool
	}{
		{"apple's shape", nonceExtension(t, nonce), true},
		{"another element first", seq(mustDER(5), tagged(1, true, octets)), true},
		{"tag 2 then tag 1", seq(tagged(2, true, octets), tagged(1, true, octets)), true},
		{"no tag 1", seq(tagged(2, true, octets)), false},
		{"tag 1 implicit", seq(tagged(1, false, nonce)), false},
		{"31 bytes", seq(tagged(1, true, mustDER(nonce[:31]))), false},
		{"tag 1 holds an integer", seq(tagged(1, true, mustDER(5))), false},
		{"tag 1 with trailing bytes", seq(tagged(1, true, append(append([]byte{}, octets...), 0x05, 0x00))), false},
		{"not a sequence", octets, false},
		{"trailing bytes", append(nonceExtension(t, nonce), 0x00), false},
		{"garbage", []byte{0x30, 0x80}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		got, ok := parseNonce(tc.der)
		if ok != tc.ok || ok && !bytes.Equal(got, nonce) {
			t.Errorf("%s: got %x %v, want ok %v", tc.name, got, ok, tc.ok)
		}
	}
}

type asrCase struct {
	name      string
	assertion []byte
	pub       []byte
	last      uint32
	want      uint32
	step      string
}

func assertionCases(t testing.TB, f *fixture) []asrCase {
	t.Helper()
	other := newKey(t, elliptic.P256())
	sign := func(counter uint32) []byte { return f.assertion(t, f.key, testAppID, counter, f.cdh) }
	ok := sign(5)
	top, err := decodeCBOR(ok)
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := top.field("signature")
	ad, _ := top.field("authenticatorData")
	c := func(name string, a []byte, last uint32, step string) asrCase {
		return asrCase{name: name, assertion: a, pub: f.point, last: last, step: step}
	}
	return []asrCase{
		{name: "first assertion", assertion: sign(1), pub: f.point, last: 0, want: 1},
		{name: "counter jumps", assertion: ok, pub: f.point, last: 3, want: 5},
		{name: "keys in the other order", assertion: cborEncode(cmap{{"authenticatorData", ad.bytes}, {"signature", sig.bytes}}), pub: f.point, last: 4, want: 5},

		c("not a map", cborEncode([]any{sig.bytes, ad.bytes}), 0, stepCBOR),
		c("trailing CBOR bytes", append(append([]byte{}, ok...), 0x00), 0, stepCBOR),
		c("no signature", cborEncode(cmap{{"authenticatorData", ad.bytes}}), 0, stepCBOR),
		c("no authenticatorData", cborEncode(cmap{{"signature", sig.bytes}}), 0, stepCBOR),
		c("signature as text", cborEncode(cmap{{"signature", "sig"}, {"authenticatorData", ad.bytes}}), 0, stepCBOR),
		c("authenticatorData too short", cborEncode(cmap{{"signature", sig.bytes}, {"authenticatorData", ad.bytes[:36]}}), 0, stepCBOR),
		c("a float inside", cborEncode(cmap{{"signature", sig.bytes}, {"authenticatorData", ad.bytes}, {"x", rawCBOR{0xf9, 0x3c, 0x00}}}), 0, stepCBOR),

		c("bad signature (another key)", f.assertion(t, other, testAppID, 5, f.cdh), 0, stepSignature),
		c("signature over another challenge", f.assertion(t, f.key, testAppID, 5, ClientDataHash("another")), 0, stepSignature),
		c("signature of another authenticatorData", cborEncode(cmap{{"signature", sig.bytes}, {"authenticatorData", binary.BigEndian.AppendUint32(bytes.Clone(ad.bytes[:33]), 6)}}), 0, stepSignature),
		{name: "stored key not a point", assertion: ok, pub: f.point[:64], step: stepSignature},
		{name: "stored key off the curve", assertion: ok, pub: append([]byte{4}, make([]byte, 64)...), step: stepSignature},

		c("rp id of another app", f.assertion(t, f.key, "TEAMID1234.dev.example.other", 5, f.cdh), 0, stepRPID),
		c("counter not rising", ok, 5, stepCounter),
		c("counter falling", ok, 9, stepCounter),
		c("counter 0 first", sign(0), 0, stepCounter),
	}
}

func TestAssertion(t *testing.T) {
	f := newFixture(t)
	for _, tc := range assertionCases(t, f) {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.verifier.Assertion(tc.assertion, tc.pub, f.cdh, tc.last)
			if tc.step == "" {
				if err != nil || got != tc.want {
					t.Fatalf("got %d, %v; want %d", got, err, tc.want)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) || Step(err) != tc.step || got != 0 {
				t.Fatalf("got %d, %v (step %q); want step %q", got, err, Step(err), tc.step)
			}
		})
	}
}

// An attested key's public key verifies its assertions end to end.
func TestAttestThenAssert(t *testing.T) {
	f := newFixture(t)
	att, err := f.verifier.Attestation(f.build(t, f.defaults()), f.keyID, f.cdh)
	if err != nil {
		t.Fatal(err)
	}
	ch2 := ClientDataHash("the second challenge")
	n, err := f.verifier.Assertion(f.assertion(t, f.key, testAppID, 1, ch2), att.PublicKey, ch2, 0)
	if err != nil || n != 1 {
		t.Fatalf("got %d, %v", n, err)
	}
	if _, err := f.verifier.Assertion(f.assertion(t, f.key, testAppID, 1, ch2), att.PublicKey, ch2, n); Step(err) != stepCounter {
		t.Fatalf("a replayed counter: %v", err)
	}
}

func TestStep(t *testing.T) {
	if Step(nil) != "" || Step(errors.New("x")) != "" {
		t.Fatal("Step of a foreign error is not empty")
	}
	wrapped := fmt.Errorf("handler: %w", invalid(stepNonce))
	if Step(wrapped) != stepNonce || !errors.Is(wrapped, ErrInvalid) {
		t.Fatalf("wrapped: %q", Step(wrapped))
	}
}

// AA4: no error holds an input. Every refusal's text is the fixed
// "appattest: invalid (<step>)" and contains none of the inputs in any form.
func TestErrorsHoldNoInput(t *testing.T) {
	f := newFixture(t)
	forbidden := []string{
		f.challenge,
		base64.StdEncoding.EncodeToString(f.keyID[:]),
		base64.RawURLEncoding.EncodeToString(f.keyID[:]),
		hex.EncodeToString(f.keyID[:]),
		base64.StdEncoding.EncodeToString(f.cdh[:]),
		hex.EncodeToString(f.cdh[:]),
		hex.EncodeToString(f.point),
		hex.EncodeToString(f.keyID[:4]),
	}
	ring, err := ParseRing(testRingLine(0x11, 1))
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := ring.Mint(testUser, f.keyID, testNow)
	forbidden = append(forbidden, tok, strings.Split(tok, ".")[2], strings.Split(tok, ".")[3])

	var errs []error
	for _, tc := range attestationCases(t, f) {
		if _, err := f.verifier.Attestation(tc.att, tc.keyID, tc.cdh); err != nil {
			errs = append(errs, err)
		}
	}
	for _, tc := range assertionCases(t, f) {
		if _, err := f.verifier.Assertion(tc.assertion, tc.pub, f.cdh, tc.last); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) < 40 {
		t.Fatalf("only %d errors collected", len(errs))
	}
	for _, err := range errs {
		if want := "appattest: invalid (" + Step(err) + ")"; err.Error() != want {
			t.Errorf("error %q, want the fixed %q", err, want)
		}
		text := fmt.Sprintf("%v|%+v|%#v|%s|%q", err, err, err, err, err)
		for _, s := range forbidden {
			if strings.Contains(text, s) {
				t.Errorf("error %q contains an input", text)
			}
		}
	}
}

// The verifier never panics on any input, and refuses what it cannot read.
func FuzzAttestation(f *testing.F) {
	fx := newFixture(f)
	f.Add(fx.build(f, fx.defaults()))
	f.Add(fx.assertion(f, fx.key, testAppID, 1, fx.cdh))
	f.Add([]byte{0xa0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if _, err := fx.verifier.Attestation(data, fx.keyID, fx.cdh); err != nil && Step(err) == "" {
			t.Fatalf("unnamed error %v", err)
		}
		if _, err := fx.verifier.Assertion(data, fx.point, fx.cdh, 0); err != nil && Step(err) == "" {
			t.Fatalf("unnamed error %v", err)
		}
	})
}
