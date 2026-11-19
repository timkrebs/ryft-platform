package ratelimit

import (
	"encoding/binary"
	"runtime"
	"testing"
	"time"
)

// A Capped keeps a bucket for at most its cap of keys (D155, moved from cmd/api's admin limit): a known key
// keeps its own, a new one beyond the cap shares the overflow bucket, and a key unused for the idle time no
// longer counts, swept at most once a second once the map is full.
func TestCappedKeepsKnownKeysAndSharesOneOverflowBucket(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	const idle = 10 * time.Minute
	// Bursts of 2 and no refill within the test: a key's own bucket has a token left after one request.
	c := NewCapped(0.0001, 2, 3, Idle(idle), Clock(func() time.Time { return now }))
	allow := func(key string) bool { ok, _ := c.Allow(key); return ok }

	for _, k := range []string{"a", "b", "c"} {
		if !allow(k) {
			t.Fatalf("%s under the cap is refused", k)
		}
	}
	// A fourth and a fifth key share the overflow bucket's two tokens; a sixth finds it empty.
	if !allow("d") || !allow("e") {
		t.Fatal("the overflow bucket's two tokens")
	}
	if ok, wait := c.Allow("f"); ok || wait <= 0 {
		t.Fatalf("a third key beyond the cap got a bucket of its own (ok %v, wait %v)", ok, wait)
	}
	// A known key still has its own bucket, with its second token.
	if !allow("a") {
		t.Fatal("a known key lost its bucket once the cap was reached")
	}
	if n := len(c.lim.buckets); n != 3 {
		t.Fatalf("%d keys in the map, the cap is 3", n)
	}
	for _, k := range []string{"d", "e", "f"} {
		if _, ok := c.lim.buckets[k]; ok {
			t.Fatalf("%s beyond the cap has a bucket in the map", k)
		}
	}

	// An idle time later the old keys no longer count: with "a" still in use, a new key gets its own bucket.
	now = now.Add(idle + time.Second)
	allow("a")
	if !allow("g") {
		t.Fatal("after the idle time a new key is refused")
	}
	if _, ok := c.lim.buckets["g"]; !ok || len(c.lim.buckets) != 2 {
		t.Fatalf("after the sweep: %d keys, want the one in use and the new one", len(c.lim.buckets))
	}

	// The sweep runs at most once a second: full again, with "h" idle, a new key within the second is overflow.
	allow("h")
	c.lim.buckets["h"].last = now.Add(-idle - time.Minute)
	now = now.Add(500 * time.Millisecond)
	allow("i")
	if _, ok := c.lim.buckets["i"]; ok {
		t.Fatal("a second sweep within the second")
	}
	// A second after the last sweep the idle "h" goes and "j" gets its own bucket.
	now = now.Add(500 * time.Millisecond)
	allow("j")
	if _, ok := c.lim.buckets["j"]; !ok {
		t.Fatal("no sweep a second after the last one")
	}
	if _, ok := c.lim.buckets["h"]; ok {
		t.Fatal("the idle key survived the sweep")
	}
}

// key16 is the i-th of many distinct keys as long as httpx.ClientKeys' (16 bytes).
func key16(i int) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[8:], uint64(i))
	return string(b[:])
}

// However many keys arrive, the map holds at most the cap (the overflow bucket is outside it): a flood of
// new addresses costs bounded memory.
func TestCappedMapStaysWithinTheCap(t *testing.T) {
	c := NewCapped(20, 50, 64)
	for i := range 10_000 {
		c.Allow(key16(i))
		if n := len(c.lim.buckets); n > 64 {
			t.Fatalf("%d keys in the map after %d new ones, the cap is 64", n, i+1)
		}
	}
}

// Idle sets the inner map's idle time too: a bucket unused for a minute is gone at the next sweep.
func TestIdleOption(t *testing.T) {
	l := New(20, 50, Idle(time.Minute))
	now := fakeClock(l)
	l.Allow("old")
	*now = now.Add(time.Minute + time.Second)
	for range sweepEvery {
		l.Allow("new")
	}
	if _, ok := l.buckets["old"]; ok {
		t.Fatal("a bucket idle for over a minute must be dropped")
	}
	if l.idle != time.Minute || New(1, 1).idle != defaultIdle {
		t.Fatalf("idle %v, default %v", l.idle, New(1, 1).idle)
	}
}

// The edge limit's memory at its cap (internal/app: 20/s, bursts of 50, 65,536 keys of 16 bytes, idle a
// minute), measured, not asserted: heap bytes depend on the runtime. The number is logged for the design.
func TestCappedMemoryAtTheEdgeCap(t *testing.T) {
	if testing.Short() {
		t.Skip("a measurement")
	}
	const keys = 65536
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	c := NewCapped(20, 50, keys, Idle(time.Minute))
	for i := range keys + 1000 { // each key a string of its own, as each request's is; the last 1,000 overflow
		c.Allow(key16(i))
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if n := len(c.lim.buckets); n != keys {
		t.Fatalf("%d keys in the map, want the cap", n)
	}
	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("%d keys at the cap: %.1f MiB of live heap (%d bytes a key, its string included)", keys,
		float64(grew)/(1<<20), grew/keys)
	runtime.KeepAlive(c)
}
