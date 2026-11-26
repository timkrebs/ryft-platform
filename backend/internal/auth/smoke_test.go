package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/config"
)

// smokeFixture is a smoke verifier over local key servers: a Supabase JWKS (ES256) and a
// Google JWKS (RS256), with the smoke account's identity.
type smokeFixture struct {
	v        Verifier
	sb       *jwksServer
	sbKey    *ecdsa.PrivateKey
	g        *googleJWKS
	gKey     *rsa.PrivateKey
	id       pushIdentity // audience, email and subject of the smoke service account
	smokeUID uuid.UUID
}

func newSmokeFixture(t *testing.T) *smokeFixture {
	t.Helper()
	f := &smokeFixture{sb: newJWKSServer(t), g: newGoogleJWKS(t), id: newPushIdentity(), smokeUID: uuid.New()}
	f.id.audience = "ryft-smoke"
	f.sbKey = f.sb.addKey(t, "sb1")
	f.gKey = newRSAKey(t)
	f.g.add("g1", f.gKey)
	sb := newVerifier(t, f.sb, SupabaseOptions{})
	g := newGoogleVerifierWith(t, GoogleOIDCOptions{Audience: f.id.audience, Email: f.id.email, Subject: f.id.subject, JWKSURL: f.g.URL})
	f.v = newSmoke(sb, g, f.smokeUID)
	return f
}

func (f *smokeFixture) token(t *testing.T, mut func(jwt.MapClaims)) string {
	t.Helper()
	c := f.id.claims()
	if mut != nil {
		mut(c)
	}
	return signRS(t, jwt.SigningMethodRS256, f.gKey, "g1", c)
}

func TestSmokeAcceptsTheSmokeAccountAsTheSmokeUser(t *testing.T) {
	f := newSmokeFixture(t)
	for _, iss := range googleIssuers {
		p, err := f.v.Verify(context.Background(), f.token(t, func(c jwt.MapClaims) { c["iss"] = iss }))
		if err != nil {
			t.Fatalf("%s: %v", iss, err)
		}
		if p != (Principal{UserID: f.smokeUID, Smoke: true}) {
			t.Fatalf("%s: the principal is the user ID and the Smoke mark, nothing else, got %+v", iss, p)
		}
	}
}

func TestSmokeRejectsEveryOtherGoogleToken(t *testing.T) {
	f := newSmokeFixture(t)
	stranger := newRSAKey(t)
	signedBy := func(key *rsa.PrivateKey, kid string, mut func(jwt.MapClaims)) string {
		c := f.id.claims()
		mut(c)
		return signRS(t, jwt.SigningMethodRS256, key, kid, c)
	}
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, f.id.claims())
	hs.Header["kid"] = "g1"
	hs256, err := hs.SignedString([]byte("not-the-key-material-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, f.id.claims()).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct{ token, reason string }{
		"wrong audience":     {f.token(t, func(c jwt.MapClaims) { c["aud"] = "https://api.example.run.app" }), "smoke_audience"},
		"two audiences":      {f.token(t, func(c jwt.MapClaims) { c["aud"] = []string{f.id.audience, "other"} }), "smoke_audience"},
		"other email":        {f.token(t, func(c jwt.MapClaims) { c["email"] = "ryft-push@ryft-test.iam.gserviceaccount.com" }), "smoke_subject"},
		"email not verified": {f.token(t, func(c jwt.MapClaims) { c["email_verified"] = false }), "smoke_subject"},
		"no email":           {f.token(t, func(c jwt.MapClaims) { delete(c, "email") }), "smoke_subject"},
		"other sub":          {f.token(t, func(c jwt.MapClaims) { c["sub"] = "1" + f.id.subject }), "smoke_subject"},
		"expired":            {f.token(t, func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() }), "smoke_expired"},
		"no exp":             {f.token(t, func(c jwt.MapClaims) { delete(c, "exp") }), anyReason},
		"HS256":              {hs256, anyReason},
		"alg none":           {none, anyReason},
		"stranger known kid": {signedBy(stranger, "g1", func(jwt.MapClaims) {}), anyReason},
		"stranger new kid":   {signedBy(stranger, "evil", func(jwt.MapClaims) {}), anyReason},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := f.v.Verify(context.Background(), tc.token)
			if err == nil {
				t.Fatalf("accepted: %+v", p)
			}
			status, reason := reasonOf(t, err)
			if status != http.StatusUnauthorized || !strings.HasPrefix(reason, "smoke_") {
				t.Fatalf("want a 401 with a smoke_ reason, got %d %q", status, reason)
			}
			if tc.reason != anyReason && reason != tc.reason {
				t.Fatalf("reason %q, want %q", reason, tc.reason)
			}
			if p != (Principal{}) {
				t.Fatalf("a refusal carries a principal: %+v", p)
			}
		})
	}
}

// A Google-looking token signed by the Supabase key still goes to the Google verifier, which has
// never seen that key: the route decides nothing about trust.
func TestSmokeWrongRouteOnlyFails(t *testing.T) {
	f := newSmokeFixture(t)
	c := claims()
	c["iss"] = "https://accounts.google.com"
	if _, err := f.v.Verify(context.Background(), sign(t, f.sbKey, "sb1", c)); err == nil {
		t.Fatal("a Supabase-signed token with a Google issuer must not verify")
	}
	// A Google-signed token with the Supabase issuer goes to Supabase, which rejects it.
	gc := f.id.claims()
	gc["iss"] = testIssuer
	_, err := f.v.Verify(context.Background(), signRS(t, jwt.SigningMethodRS256, f.gKey, "g1", gc))
	if status, reason := reasonOf(t, err); status != http.StatusUnauthorized || strings.HasPrefix(reason, "smoke_") {
		t.Fatalf("want Supabase's 401, got %d %q", status, reason)
	}
	// An issuer that only starts like Google's is not Google's: a Supabase-signed token with it
	// is Supabase's to judge, and fails on its issuer.
	c = claims()
	c["iss"] = "https://accounts.google.com.evil.example"
	_, err = f.v.Verify(context.Background(), sign(t, f.sbKey, "sb1", c))
	if status, reason := reasonOf(t, err); status != http.StatusUnauthorized || reason != "issuer" {
		t.Fatalf("want Supabase's 401 issuer, got %d %q", status, reason)
	}
}

func TestSmokeLeavesSupabaseTokensAlone(t *testing.T) {
	f := newSmokeFixture(t)
	c := claims()
	p, err := f.v.Verify(context.Background(), sign(t, f.sbKey, "sb1", c))
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID.String() != c["sub"] || p.Email != "tim@example.com" || p.Provider != "apple" || p.Name != "Tim K" || p.UserID == f.smokeUID {
		t.Fatalf("principal %+v", p)
	}
	for name, token := range map[string]string{
		"garbage":         "not-a-jwt",
		"empty":           "",
		"bad payload":     "a.b.c",
		"payload no JSON": "a." + "bm90LWpzb24" + ".c",
		"too many parts":  "a.b.c.d",
	} {
		_, err := f.v.Verify(context.Background(), token)
		status, reason := reasonOf(t, err)
		if status != http.StatusUnauthorized || strings.HasPrefix(reason, "smoke_") {
			t.Errorf("%s: want Supabase's 401, got %d %q", name, status, reason)
		}
	}
}

func TestSmokeOffRefusesGoogleTokens(t *testing.T) {
	f := newSmokeFixture(t)
	// What FromConfig returns with the smoke sign-in off: the Supabase verifier alone.
	off := newVerifier(t, f.sb, SupabaseOptions{})
	_, err := off.Verify(context.Background(), f.token(t, nil))
	if status, _ := reasonOf(t, err); status != http.StatusUnauthorized {
		t.Fatalf("a Google token with smoke off must be a 401, got %v", err)
	}

	cfg := config.Auth{Mode: config.AuthSupabase, Issuer: testIssuer, JWKSURL: f.sb.URL}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	v, err := FromConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(*Supabase); !ok {
		t.Fatalf("smoke off: FromConfig returns the Supabase verifier as before, got %T", v)
	}
	cfg.SmokeSAEmail, cfg.SmokeSAID, cfg.SmokeAudience, cfg.SmokeUserID = f.id.email, f.id.subject, f.id.audience, f.smokeUID
	if v, err = FromConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(*smokeVerifier); !ok {
		t.Fatalf("smoke on: FromConfig wraps the verifier, got %T", v)
	}
}

func TestSmokeReadyIsSupabasesOnly(t *testing.T) {
	f := newSmokeFixture(t)
	if err := f.v.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Google's keys never load: Ready stays ready, and a smoke token is a 503.
	sb := newVerifier(t, f.sb, SupabaseOptions{})
	dead := newGoogleJWKS(t)
	dead.set(true, false)
	g := newGoogleVerifierWith(t, GoogleOIDCOptions{Audience: f.id.audience, Email: f.id.email, Subject: f.id.subject, JWKSURL: dead.URL})
	v := newSmoke(sb, g, f.smokeUID)
	if err := v.Ready(context.Background()); err != nil {
		t.Fatalf("a Google JWKS outage must not make the service unready: %v", err)
	}
	if err := g.Ready(context.Background()); err == nil {
		t.Fatal("fixture: Google's keys should be missing")
	}
	_, err := v.Verify(context.Background(), f.token(t, nil))
	if status, reason := reasonOf(t, err); status != http.StatusServiceUnavailable || reason != "smoke_jwks_unavailable" {
		t.Fatalf("want 503 smoke_jwks_unavailable, got %d %q", status, reason)
	}

	// And the other way round: Supabase's keys missing is unready whatever Google has.
	down := newJWKSServer(t)
	down.setFailing(true)
	sbDown := newVerifier(t, down, SupabaseOptions{})
	if err := newSmoke(sbDown, g, f.smokeUID).Ready(context.Background()); err == nil {
		t.Fatal("Ready must follow the Supabase verifier")
	}
}
