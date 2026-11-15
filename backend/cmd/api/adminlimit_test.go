package main

import (
	"fmt"
	"testing"
	"time"

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
