package db

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"testing"
	"time"
)

// Supabase's root CA, public, which backend/Dockerfile bakes into every image as
// /etc/ryft/supabase-ca.crt: the sslrootcert of DATABASE_URL on Cloud Run (verify-full, D221,
// design §4). Pinned byte for byte, so a swap is a reviewed change of this hash.
const (
	supabaseCAFile   = "../../certs/supabase-ca.crt"
	supabaseCASHA256 = "700723581420dd1ac98fd7e9ac529f0ef210eadcaf87fc868a3ad7d114c2f3b7"
)

// supabaseCAWarn is how long before the CA's expiry this test starts to fail: time to put the
// successor into the image (and this test) before every connection to Supabase would.
const supabaseCAWarn = 90 * 24 * time.Hour

func TestSupabaseCA(t *testing.T) {
	b, err := os.ReadFile(supabaseCAFile)
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != supabaseCASHA256 {
		t.Fatalf("%s has SHA-256 %x, want %s: the CA changed", supabaseCAFile, sum, supabaseCASHA256)
	}
	block, rest := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatal("want exactly one PEM CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case !cert.BasicConstraintsValid || !cert.IsCA:
		t.Error("not a CA certificate (basic constraints)")
	case cert.KeyUsage&x509.KeyUsageCertSign == 0:
		t.Error("its key usage does not allow signing certificates")
	case cert.Subject.CommonName != "Supabase Root 2021 CA" || cert.Subject.String() != cert.Issuer.String():
		t.Errorf("subject %q, issuer %q: want the self-issued Supabase Root 2021 CA", cert.Subject, cert.Issuer)
	}
	if err := cert.CheckSignatureFrom(cert); err != nil {
		t.Errorf("not self-signed: %v", err)
	}
	if want := time.Date(2031, 4, 26, 10, 56, 53, 0, time.UTC); !cert.NotAfter.Equal(want) {
		t.Errorf("valid until %s, want %s", cert.NotAfter, want)
	}
	if left := time.Until(cert.NotAfter); left < supabaseCAWarn {
		t.Errorf("the Supabase CA expires on %s (%s left): get its successor from the dashboard (Database → Settings → "+
			"SSL Configuration), put it into backend/certs and this test", cert.NotAfter.Format(time.DateOnly), left.Round(time.Hour))
	}
	if !x509.NewCertPool().AppendCertsFromPEM(b) {
		t.Error("a cert pool does not take it (pgx builds its RootCAs the same way)")
	}
}
