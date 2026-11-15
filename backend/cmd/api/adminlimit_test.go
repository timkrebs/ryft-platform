package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/ratelimit"
)

// The admin limit keeps a bucket for at most max addresses (D155): a known address keeps its own, a new one
// beyond the cap shares the overflow bucket, and an address unseen for adminAddrIdle no longer counts.
func TestAdminAddressLimitIsCapped(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	l := newAddrLimit(ratelimit.New(0.001, 1), 3, clock) // a burst of 1 and no refill within the test

	for _, addr := range []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"} {
		if k := l.key(addr); k != addr {
			t.Fatalf("%s under the cap is keyed %q", addr, k)
		}
	}
	if k := l.key("203.0.113.4"); k != adminOverflow {
		t.Fatalf("a fourth address is keyed %q, want the overflow bucket", k)
	}
	if k := l.key("203.0.113.1"); k != "203.0.113.1" {
		t.Fatalf("a known address is keyed %q once the cap is reached", k)
	}
	if len(l.seen) != 3 {
		t.Fatalf("%d addresses tracked, the cap is 3", len(l.seen))
	}
	// The overflow bucket is one bucket: two new addresses beyond the cap share its one token.
	if ok, _ := l.Allow("198.51.100.1"); !ok {
		t.Fatal("the overflow bucket's first token")
	}
	if ok, wait := l.Allow("198.51.100.2"); ok || wait <= 0 {
		t.Fatalf("a second address beyond the cap got its own bucket (ok %v, wait %v)", ok, wait)
	}

	// Ten idle minutes later the old addresses no longer count: a new one gets its own bucket.
	now = now.Add(adminAddrIdle + time.Second)
	l.seen["203.0.113.1"] = now // still in use
	if k := l.key("203.0.113.5"); k != "203.0.113.5" {
		t.Fatalf("after the idle time a new address is keyed %q", k)
	}
	if len(l.seen) != 2 {
		t.Fatalf("%d addresses tracked after the sweep, want the one in use and the new one", len(l.seen))
	}
	// The sweep runs at most once a second: with the set full again and nothing idle, a new address within
	// the second is overflow without a second scan.
	l.key("203.0.113.6")
	swept := l.swept
	if k := l.key("203.0.113.7"); k != adminOverflow || l.swept != swept {
		t.Fatalf("keyed %q, swept again %v", k, l.swept != swept)
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
	au.limiter = newAddrLimit(ratelimit.New(0.001, 1), 2, func() time.Time { return now })
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
