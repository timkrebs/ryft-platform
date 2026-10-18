package main

import (
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

func TestInsightCache(t *testing.T) {
	var c insightCache
	t0 := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	if _, ok := c.get("2026-10-18", t0); ok {
		t.Fatal("empty cache must miss")
	}
	cards := []model.Insight{{ID: "sleep-debt"}}
	c.put("2026-10-18", t0, cards)
	if got, ok := c.get("2026-10-18", t0.Add(4*time.Minute)); !ok || got[0].ID != "sleep-debt" {
		t.Fatal("fresh entry must hit")
	}
	if _, ok := c.get("2026-10-18", t0.Add(insightsTTL)); ok {
		t.Fatal("entry older than the TTL must miss")
	}
	if _, ok := c.get("2026-10-19", t0.Add(time.Minute)); ok {
		t.Fatal("a new local day must miss")
	}
	c.put("2026-10-18", t0, []model.Insight{})
	if got, ok := c.get("2026-10-18", t0); !ok || len(got) != 0 {
		t.Fatal("an empty result is cached too")
	}
}
