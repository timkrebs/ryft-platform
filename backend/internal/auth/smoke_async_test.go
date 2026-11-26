package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/config"
)

// stalledJWKS is a Google certs endpoint that holds every request until unblock is called (or the
// client gives up), then serves the one key.
type stalledJWKS struct {
	*httptest.Server
	release chan struct{}
	once    sync.Once
}

func newStalledJWKS(t *testing.T, jwk map[string]string) *stalledJWKS {
	t.Helper()
	s := &stalledJWKS{release: make(chan struct{})}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-s.release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{jwk}})
	}))
	t.Cleanup(s.Close)
	t.Cleanup(s.unblock) // runs before Close, which waits for the handlers
	return s
}

func (s *stalledJWKS) unblock() { s.once.Do(func() { close(s.release) }) }

// D286: a stalled Google JWKS must not hold up FromConfig. Supabase tokens verify at once, a smoke
// token is a 503 smoke_jwks_unavailable until Google's keys are in, and then it verifies.
func TestFromConfigSmokeDoesNotWaitForGoogleJWKS(t *testing.T) {
	sb := newJWKSServer(t)
	sbKey := sb.addKey(t, "sb1")
	id := newPushIdentity()
	id.audience = "ryft-smoke"
	gKey := newRSAKey(t)
	stalled := newStalledJWKS(t, rsaJWK("g1", &gKey.PublicKey))
	smokeUID := uuid.New()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := config.Auth{Mode: config.AuthSupabase, Issuer: testIssuer, JWKSURL: sb.URL,
		SmokeSAEmail: id.email, SmokeSAID: id.subject, SmokeAudience: id.audience, SmokeUserID: smokeUID}

	start := time.Now()
	v, err := fromConfig(ctx, cfg, stalled.URL)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("fromConfig waited %v for Google's JWKS", d)
	}

	c := claims()
	if p, err := v.Verify(ctx, sign(t, sbKey, "sb1", c)); err != nil || p.Smoke || p.UserID.String() != c["sub"] {
		t.Fatalf("a Supabase token must verify while Google stalls: %+v %v", p, err)
	}
	if err := v.Ready(ctx); err != nil {
		t.Fatalf("Ready is Supabase's: %v", err)
	}
	smokeToken := signRS(t, jwt.SigningMethodRS256, gKey, "g1", id.claims())
	vctx, vcancel := context.WithTimeout(ctx, 200*time.Millisecond) // a smoke request must not hang on the stall either
	defer vcancel()
	_, err = v.Verify(vctx, smokeToken)
	if status, reason := reasonOf(t, err); status != http.StatusServiceUnavailable || reason != "smoke_jwks_unavailable" {
		t.Fatalf("want 503 smoke_jwks_unavailable, got %d %q", status, reason)
	}

	stalled.unblock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, err := v.Verify(ctx, smokeToken)
		if err == nil {
			if p != (Principal{UserID: smokeUID, Smoke: true}) {
				t.Fatalf("principal %+v", p)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the smoke token never verified after Google answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A smoke configuration without its identity fails at start, not silently in the background.
func TestFromConfigSmokeRequiresItsIdentity(t *testing.T) {
	sb := newJWKSServer(t)
	cfg := config.Auth{Mode: config.AuthSupabase, Issuer: testIssuer, JWKSURL: sb.URL, SmokeUserID: uuid.New()}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if _, err := fromConfig(ctx, cfg, ""); err == nil {
		t.Fatal("a smoke user without audience, email and unique ID must be refused at start")
	}
}
