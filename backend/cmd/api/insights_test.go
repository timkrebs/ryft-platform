package main

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestInsightCache(t *testing.T) {
	c := newInsightCache()
	t0 := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	alice := insightKey{user: uuid.New(), day: "2026-10-18"}
	if _, ok := c.get(alice, t0); ok {
		t.Fatal("empty cache must miss")
	}
	c.put(alice, t0, []model.Insight{{ID: "sleep-debt"}})
	if got, ok := c.get(alice, t0.Add(4*time.Minute)); !ok || got[0].ID != "sleep-debt" {
		t.Fatal("fresh entry must hit")
	}
	if _, ok := c.get(alice, t0.Add(insightsTTL)); ok {
		t.Fatal("entry older than the TTL must miss")
	}
	if _, ok := c.get(insightKey{user: alice.user, day: "2026-10-19"}, t0.Add(time.Minute)); ok {
		t.Fatal("a new local day must miss")
	}
	bob := insightKey{user: uuid.New(), day: "2026-10-18"}
	if _, ok := c.get(bob, t0.Add(time.Minute)); ok {
		t.Fatal("another user must never get Alice's cards")
	}
	c.put(alice, t0, []model.Insight{})
	if got, ok := c.get(alice, t0); !ok || len(got) != 0 {
		t.Fatal("an empty result is cached too")
	}
}

func TestInsightCacheIsBounded(t *testing.T) {
	c := newInsightCache()
	t0 := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	for k := 0; k < insightCacheMax+10; k++ {
		c.put(insightKey{user: uuid.New(), day: "2026-10-18"}, t0, nil)
	}
	if n := len(c.entries); n > insightCacheMax {
		t.Fatalf("cache grew to %d entries", n)
	}
}
