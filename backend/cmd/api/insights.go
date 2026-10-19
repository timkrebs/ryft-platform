package main

import (
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/insights"
	"github.com/timkrebs/ryft/internal/model"
)

// insightsTTL is how long computed insight cards are served from memory.
// Scores change at most every 15 minutes (scoring's timer) or after a sync.
const insightsTTL = 5 * time.Minute

// insightCacheMax bounds memory; past it, expired entries are evicted and,
// if that is not enough, the cache starts over.
const insightCacheMax = 10_000

// insightKey scopes a cache entry to one user and one local day.
type insightKey struct {
	user uuid.UUID
	day  string
}

type insightEntry struct {
	at    time.Time
	cards []model.Insight
}

// insightCache holds each user's cards for their current day, per pod.
type insightCache struct {
	mu      sync.Mutex
	entries map[insightKey]insightEntry
}

func newInsightCache() *insightCache {
	return &insightCache{entries: map[insightKey]insightEntry{}}
}

func (c *insightCache) get(k insightKey, now time.Time) ([]model.Insight, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok || now.Sub(e.at) >= insightsTTL {
		return nil, false
	}
	return e.cards, true
}

func (c *insightCache) put(k insightKey, now time.Time, cards []model.Insight) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= insightCacheMax {
		for key, e := range c.entries {
			if now.Sub(e.at) >= insightsTTL {
				delete(c.entries, key)
			}
		}
		if len(c.entries) >= insightCacheMax {
			clear(c.entries)
		}
	}
	c.entries[k] = insightEntry{at: now, cards: cards}
}

// forget drops a user's entries (their account was deleted).
func (c *insightCache) forget(user uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if k.user == user {
			delete(c.entries, k)
		}
	}
}

// insights is the Insights screen: rule-based cards about today, computed from
// the last insights.Window days of scores on read.
func (h *handler) insights(w http.ResponseWriter, r *http.Request) {
	loc, err := h.location(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	now := time.Now()
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	key := today.Format(dayLayout)
	ck := insightKey{user: userOf(r).ID, day: key}
	if cards, ok := h.cache.get(ck, now); ok {
		httpx.WriteJSON(w, 200, cards)
		return
	}

	from := today.AddDate(0, 0, -(insights.Window - 1)).Format(dayLayout)
	scores, err := h.st.DailyScores(r.Context(), userOf(r).ID, from, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	nights, err := h.st.SleepNights(r.Context(), userOf(r).ID, from, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	bedtimes := make(map[string]time.Time, len(nights))
	for _, n := range nights {
		if n.Start != nil {
			bedtimes[n.Day] = n.Start.In(loc)
		}
	}
	// Records of today and yesterday (the only ones a card is about).
	recent, err := h.st.PRsSince(r.Context(), userOf(r).ID, today.AddDate(0, 0, -1))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	prs := map[string][]insights.PR{}
	for _, p := range recent {
		d := p.AchievedAt.In(loc).Format(dayLayout)
		prs[d] = append(prs[d], insights.PR{Exercise: p.Exercise, Kind: p.Kind, Value: p.Value})
	}
	cards := insights.Evaluate(insights.NewHistory(today, insights.Window, scores, bedtimes).WithPRs(prs))
	h.cache.put(ck, now, cards)
	httpx.WriteJSON(w, 200, cards)
}
