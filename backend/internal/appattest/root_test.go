package appattest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"testing"
	"time"
)

// appleRootSHA256 pins the embedded root (apple_root.pem). The PEM was written
// from memory, not downloaded; before the warn PR the user cross-checks this
// value against Apple's file:
//
//	curl -s https://www.apple.com/certificateauthority/Apple_App_Attestation_Root_CA.pem | openssl x509 -noout -fingerprint -sha256
//
// (openssl prints it upper case with colons.)
const appleRootSHA256 = "1cb9823ba28ba6ad2d33a006941de2ae4f513ef1d4e831b9f7e0fa7b6242c932"

func TestAppleRoot(t *testing.T) {
	cert, err := appleRoot()
	if err != nil {
		t.Fatal(err)
	}
	// A self-signed root verifies under its own key: a single wrong byte fails here.
	if err := cert.CheckSignatureFrom(cert); err != nil {
		t.Fatalf("the embedded root's self-signature does not verify (the PEM is wrong): %v", err)
	}
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		t.Fatalf("self-signature: %v", err)
	}
	if cn := cert.Subject.CommonName; cn != "Apple App Attestation Root CA" {
		t.Errorf("subject CN %q", cn)
	}
	if o := cert.Subject.Organization; len(o) != 1 || o[0] != "Apple Inc." {
		t.Errorf("subject O %q", o)
	}
	if !bytes.Equal(cert.RawSubject, cert.RawIssuer) {
		t.Error("not self-issued")
	}
	if !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Errorf("not a CA: IsCA %v, key usage %v", cert.IsCA, cert.KeyUsage)
	}
	day := func(tm time.Time) string { return tm.UTC().Format(time.DateOnly) }
	if day(cert.NotBefore) != "2020-03-18" || day(cert.NotAfter) != "2045-03-15" {
		t.Errorf("validity %v to %v, want 2020-03-18 to 2045-03-15", cert.NotBefore, cert.NotAfter)
	}
	if pub, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || pub.Curve != elliptic.P384() {
		t.Errorf("public key %T, want ECDSA P-384", cert.PublicKey)
	}
	sum := sha256.Sum256(cert.Raw)
	if got := hex.EncodeToString(sum[:]); got != appleRootSHA256 {
		t.Errorf("SHA-256 fingerprint %s, pinned %s", got, appleRootSHA256)
	}
}

// One changed byte in the embedded root is caught by the self-signature.
func TestAppleRootTamper(t *testing.T) {
	cert, err := appleRoot()
	if err != nil {
		t.Fatal(err)
	}
	tbs := bytes.Clone(cert.RawTBSCertificate)
	tbs[len(tbs)/2] ^= 0x01
	if err := cert.CheckSignature(cert.SignatureAlgorithm, tbs, cert.Signature); err == nil {
		t.Fatal("a flipped bit still verifies")
	}
}
