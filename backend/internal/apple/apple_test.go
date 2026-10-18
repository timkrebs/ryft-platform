package apple

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func testKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

type fakeApple struct {
	*httptest.Server
	forms  map[string]url.Values
	status int
	body   string
}

func newFakeApple(t *testing.T) *fakeApple {
	f := &fakeApple{forms: map[string]url.Values{}, status: 200}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.forms[r.URL.Path] = r.PostForm
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(f.Close)
	return f
}

func client(t *testing.T, f *fakeApple) (*Client, *ecdsa.PrivateKey) {
	t.Helper()
	k, p8 := testKey(t)
	c, err := New(Config{TeamID: "M77SKHKG63", KeyID: "KEY123", ClientID: "dev.timkrebs.ryft", PrivateKey: p8}, f.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.base = f.URL
	return c, k
}

func TestClientSecretIsAppleShaped(t *testing.T) {
	f := newFakeApple(t)
	c, k := client(t, f)
	c.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	s, err := c.clientSecret()
	if err != nil {
		t.Fatal(err)
	}
	var claims jwt.RegisteredClaims
	tok, err := jwt.ParseWithClaims(s, &claims, func(*jwt.Token) (any, error) { return &k.PublicKey, nil },
		jwt.WithValidMethods([]string{"ES256"}), jwt.WithTimeFunc(c.now), jwt.WithAudience("https://appleid.apple.com"))
	if err != nil {
		t.Fatal(err)
	}
	if tok.Header["kid"] != "KEY123" || claims.Issuer != "M77SKHKG63" || claims.Subject != "dev.timkrebs.ryft" ||
		claims.ExpiresAt.Sub(claims.IssuedAt.Time) != 5*time.Minute {
		t.Fatalf("header %v claims %+v", tok.Header, claims)
	}
}

func TestExchange(t *testing.T) {
	f := newFakeApple(t)
	c, _ := client(t, f)
	idToken, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "001234.abcdef"}).SignedString([]byte("x"))
	f.body = `{"access_token":"a","refresh_token":"r.secret","id_token":"` + idToken + `"}`
	got, err := c.Exchange(t.Context(), "c.code")
	if err != nil || got.RefreshToken != "r.secret" || got.Subject != "001234.abcdef" {
		t.Fatalf("%+v %v", got, err)
	}
	form := f.forms["/auth/token"]
	if form.Get("code") != "c.code" || form.Get("grant_type") != "authorization_code" ||
		form.Get("client_id") != "dev.timkrebs.ryft" || form.Get("client_secret") == "" {
		t.Fatalf("form %v", form)
	}

	f.status, f.body = 400, `{"error":"invalid_grant"}`
	if _, err := c.Exchange(t.Context(), "used"); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("invalid_grant: %v", err)
	}
	f.body = `{"error":"invalid_client"}`
	if _, err := c.Exchange(t.Context(), "c"); !errors.Is(err, ErrInvalidClient) {
		t.Fatalf("invalid_client: %v", err)
	}
	f.status, f.body = 503, ``
	if _, err := c.Exchange(t.Context(), "c"); err == nil || errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("an outage is a plain error: %v", err)
	}
}

func TestRevoke(t *testing.T) {
	f := newFakeApple(t)
	c, _ := client(t, f)
	if err := c.Revoke(t.Context(), "r.secret"); err != nil {
		t.Fatal(err)
	}
	form := f.forms["/auth/revoke"]
	if form.Get("token") != "r.secret" || form.Get("token_type_hint") != "refresh_token" || form.Get("client_id") != "dev.timkrebs.ryft" {
		t.Fatalf("form %v", form)
	}
	f.status, f.body = 400, `{"error":"invalid_client"}`
	if err := c.Revoke(t.Context(), "r"); !errors.Is(err, ErrInvalidClient) {
		t.Fatalf("%v", err)
	}
}

func TestNewAcceptsPEMOrBase64AndRefusesOthers(t *testing.T) {
	_, p8 := testKey(t)
	for name, key := range map[string]string{"pem": p8, "base64": base64.StdEncoding.EncodeToString([]byte(p8))} {
		if _, err := New(Config{TeamID: "T", KeyID: "K", ClientID: "C", PrivateKey: key}, nil); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, cfg := range map[string]Config{
		"no team":   {KeyID: "K", ClientID: "C", PrivateKey: p8},
		"garbage":   {TeamID: "T", KeyID: "K", ClientID: "C", PrivateKey: "not a key"},
		"empty key": {TeamID: "T", KeyID: "K", ClientID: "C"},
	} {
		if _, err := New(cfg, nil); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}
