package main

import (
	"net/http"
	"sync"
	"time"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/insights"
	"github.com/timkrebs/ryft/internal/model"
)

// insightsTTL is how long computed insight cards are served from memory.
// Scores change at most every 15 minutes (scoring's timer) or after a sync.
const insightsTTL = 5 * time.Minute

// insightCache holds today's cards for one user (this service is single-user).
type insightCache struct {
	mu    sync.Mutex
	day   string
	at    time.Time
	cards []model.Insight
}

func (c *insightCache) get(day string, now time.Time) ([]model.Insight, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cards == nil || c.day != day || now.Sub(c.at) >= insightsTTL {
		return nil, false
	}
	return c.cards, true
}

func (c *insightCache) put(day string, now time.Time, cards []model.Insight) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.day, c.at, c.cards = day, now, cards
}

// insights is the Insights screen: rule-based cards about today, computed from
// the last insights.Window days of scores on read.
func (h *handler) insights(w http.ResponseWriter, r *http.Request) {
	loc, err := h.location(r)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	now := time.Now()
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	key := today.Format(dayLayout)
	if cards, ok := h.cache.get(key, now); ok {
		httpx.WriteJSON(w, 200, cards)
		return
	}

	from := today.AddDate(0, 0, -(insights.Window - 1)).Format(dayLayout)
	scores, err := h.st.DailyScores(r.Context(), h.cfg.UserID, from, key)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	nights, err := h.st.SleepNights(r.Context(), h.cfg.UserID, from, key)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	bedtimes := make(map[string]time.Time, len(nights))
	for _, n := range nights {
		if n.Start != nil {
			bedtimes[n.Day] = n.Start.In(loc)
		}
	}
	cards := insights.Evaluate(insights.NewHistory(today, insights.Window, scores, bedtimes))
	h.cache.put(key, now, cards)
	httpx.WriteJSON(w, 200, cards)
}
