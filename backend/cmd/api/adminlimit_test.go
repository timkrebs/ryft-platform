package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/ratelimit"
)

// The cap itself (a known key keeps its bucket, new ones beyond it share one, the idle sweep) is
// ratelimit.Capped's and tested there (TestCappedKeepsKnownKeysAndSharesOneOverflowBucket).

// adminKey.presented is the edge limit's exemption for the break-glass key (Spec.LimitExempt): the
// configured key only, on /v1/admin/ only, and nothing at all when no key is set.
func TestAdminKeyPresented(t *testing.T) {
	key := newAdminKey(testAdminKey)
	req := func(path, authorization string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		return r
	}
	bearer := "Bearer " + testAdminKey
	for _, c := range []struct {
		name string
		key  adminKey
		r    *http.Request
		want bool
	}{
		{"the key on an admin route", key, req("/v1/admin/flags", bearer), true},
		{"the key on a deeper admin route", key, req("/v1/admin/users/abcd1234/access", bearer), true},
		{"the key on a user route", key, req("/v1/me", bearer), false},
		{"the key on the tick", key, req("/internal/tick", bearer), false},
		{"the key on a look-alike path", key, req("/v1/adminx/flags", bearer), false},
		{"the key without its scheme", key, req("/v1/admin/flags", testAdminKey), false},
		{"a wrong key of the same length", key, req("/v1/admin/flags", "Bearer "+strings.ToUpper(testAdminKey)), false},
		{"a prefix of the key", key, req("/v1/admin/flags", "Bearer "+testAdminKey[:len(testAdminKey)-1]), false},
		{"no bearer", key, req("/v1/admin/flags", ""), false},
		{"no key set, no bearer", newAdminKey(""), req("/v1/admin/flags", ""), false},
		{"no key set, an empty bearer", newAdminKey(""), req("/v1/admin/flags", "Bearer "), false},
		{"no key set, a bearer", newAdminKey(""), req("/v1/admin/flags", "Bearer x"), false},
	} {
		if got := c.key.presented(c.r); got != c.want {
			t.Errorf("%s: presented %v, want %v", c.name, got, c.want)
		}
	}
	// The admin chain and the edge compare the same way.
	au := newAdminAuth(quietHandler(), adminOptions{Key: testAdminKey}, func(*http.Request) string { return "" })
	if !au.isKey(testAdminKey) || au.isKey(strings.ToUpper(testAdminKey)) || au.isKey("") {
		t.Error("adminAuth.isKey disagrees with adminKey.is")
	}
}

// The api's Spec.LimitExempt reads the ADMIN_API_KEY that Load read: before Load nothing is exempt, after
// it the key on /v1/admin/ is, and only that.
func TestSpecLimitExemptUsesTheLoadedKey(t *testing.T) {
	for k, v := range map[string]string{
		"K_SERVICE": "", "CLOUD_RUN_JOB": "", "AUTH_MODE": "apikey", "ALLOW_INSECURE_APIKEY_AUTH": "true",
		"API_KEY": strings.Repeat("k", 16), "USER_ID": "", "PRIVACY_POLICY_VERSION": "2026-01-01", "PRIVACY_POLICY_URL": "",
		"BETA_GATE": "", "CONSENT_ENFORCE": "", "ADMIN_API_KEY": testAdminKey, "ADMIN_WEB_ORIGINS": "", "PUBLIC_WEB_URL": "",
		"POSTHOG_HOST": "", "POSTHOG_PROJECT_KEY": "",
	} {
		t.Setenv(k, v)
	}
	s := spec()
	if s.LimitExempt == nil {
		t.Fatal("the api has no LimitExempt")
	}
	admin := httptest.NewRequest(http.MethodGet, "/v1/admin/flags", nil)
	admin.Header.Set("Authorization", "Bearer "+testAdminKey)
	if s.LimitExempt(admin) {
		t.Error("exempt before Load set the key")
	}
	if err := s.Load(slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if !s.LimitExempt(admin) {
		t.Error("the loaded key on /v1/admin/ is not exempt")
	}
	user := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	user.Header.Set("Authorization", "Bearer "+testAdminKey)
	if s.LimitExempt(user) {
		t.Error("the key on a user route is exempt")
	}
}

// Through the admin chain with the real cap and the default limit (10/s, bursts of 20): once maxAdminAddrs
// addresses are known, the next ones share one bucket and get 429 together, and a known address still has
// its own.
func TestAdminLimitCapThroughTheChain(t *testing.T) {
	srv := adminRoutes(quietHandler(), adminOptions{Key: testAdminKey, Accounts: newFakeAccounts(), Timeout: time.Second})
	addr := func(i int) string { return fmt.Sprintf("2001:db8:%x:%x::1", i>>16, i&0xffff) } // one /64 each
	hit := func(xff string) int { return do(srv, "GET", "/v1/admin/flags", "x", "", "X-Forwarded-For", xff).status }
	for i := range maxAdminAddrs {
		if s := hit(addr(i)); s != 401 {
			t.Fatalf("address %d under the cap: %d", i, s)
		}
	}
	var refused int
	for i := range 25 {
		if hit(addr(maxAdminAddrs+i)) == 429 {
			refused++
		}
	}
	if refused != 5 {
		t.Errorf("%d of 25 new addresses beyond the cap refused, want 5: they share one bucket of 20", refused)
	}
	if s := hit(addr(0)); s != 401 {
		t.Errorf("a known address beyond the cap: %d, want its own bucket", s)
	}
}

// The operator's break-glass key is checked before the limit (in constant time, as require checks it): a
// flood from one IPv6 /48 that holds every slot and drains the overflow bucket does not lock it out, and it
// takes no token. A wrong key is just another bearer and is limited as before, and so is the Supabase
// token path.
func TestAdminKeyPassesTheLimit(t *testing.T) {
	jwks := newJWKSStub(t, false)
	op := uuid.New()
	au := newAdminAuth(quietHandler(), adminOptions{Key: testAdminKey, Verifier: jwks.verifier(t), Accounts: newFakeAccounts(op)},
		func(*http.Request) string { return "" })
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	// Two addresses with a bucket of their own, a burst of 1 each and no refill within the test.
	au.limiter = ratelimit.NewCapped(0.001, 1, 2, ratelimit.Clock(func() time.Time { return now }))
	var actors []string
	chain := au.limit(au.require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if act, ok := adminActorFrom(r.Context()); ok {
			actors = append(actors, act.kind())
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	hit := func(bearer, xff string) int {
		return do(chain, "GET", "/v1/admin/flags", bearer, "", "X-Forwarded-For", xff).status
	}
	wrongKey := strings.ToUpper(testAdminKey) // as long as the key, and not it
	tok := jwks.sign(t, adminClaims(op))
	limited, keyPassed := count(rateLimited("admin_ip")), count(adminAuthorized(model.ActorKey))

	// One /48: two /64s spend their buckets, a third the shared overflow bucket's one token.
	for _, a := range []string{"2001:db8:1:1::1", "2001:db8:1:2::1", "2001:db8:1:3::1"} {
		if s := hit(wrongKey, a); s != http.StatusUnauthorized {
			t.Fatalf("a wrong key from %s: %d, want 401 (it takes a token)", a, s)
		}
	}
	// Every bucket is empty: from a known address and from a new one (overflow), a wrong key, an
	// operator's token and garbage get 429.
	for _, a := range []string{"2001:db8:1:1::1", "2001:db8:1:4::1"} {
		for name, b := range map[string]string{"a wrong key": wrongKey, "a token": tok, "garbage": "x"} {
			if s := hit(b, a); s != http.StatusTooManyRequests {
				t.Errorf("%s from %s with the buckets empty: %d, want 429", name, a, s)
			}
		}
	}
	if got := count(rateLimited("admin_ip")) - limited; got != 6 {
		t.Errorf("ryft_rate_limited_total{class=admin_ip} rose by %v, want 6", got)
	}
	// The key passes from all of them, again and again, and is still counted as the key.
	before := count(rateLimited("admin_ip"))
	for range 5 {
		for _, a := range []string{"2001:db8:1:1::1", "2001:db8:1:4::1", "2001:db8:1:5::1"} {
			if s := hit(testAdminKey, a); s != http.StatusNoContent {
				t.Fatalf("the key from %s with the buckets empty: %d, want it through", a, s)
			}
		}
	}
	if got := count(rateLimited("admin_ip")); got != before {
		t.Errorf("the key was counted as rate limited: %v more", got-before)
	}
	if got := count(adminAuthorized(model.ActorKey)) - keyPassed; got != 15 || len(actors) != 15 {
		t.Errorf("the key passed %v times (%d actors), want 15", got, len(actors))
	}
	for _, a := range actors {
		if a != model.ActorKey {
			t.Errorf("an actor %q passed, want only the key", a)
		}
	}
	// The key's requests refilled nothing: a wrong key is still limited.
	if s := hit(wrongKey, "2001:db8:1:1::1"); s != http.StatusTooManyRequests {
		t.Errorf("a wrong key after the key's requests: %d, want 429", s)
	}
}
