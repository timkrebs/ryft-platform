package main

import (
	"net/http"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
)

// trendRanges are the Trends chart windows, in days ending at `to`.
var trendRanges = map[string]int{"7d": 7, "30d": 30, "90d": 90}

func (h *handler) trends(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	metric := q.Get("metric")
	if !model.KnownTrendMetrics[metric] {
		httpx.WriteError(w, 422, "metric must be one of readiness, recovery, strain, sleep, sleep_min, hrv, rhr")
		return
	}
	rng := q.Get("range")
	if rng == "" {
		rng = "7d"
	}
	n, ok := trendRanges[rng]
	if !ok {
		httpx.WriteError(w, 422, "range must be 7d, 30d or 90d")
		return
	}
	loc, err := h.location(r)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	end, err := endDay(r, loc)
	if err != nil {
		httpx.WriteError(w, 400, err.Error())
		return
	}
	from, to := end.AddDate(0, 0, -(n-1)).Format(dayLayout), end.Format(dayLayout)
	points, baseline, err := h.st.Trend(r.Context(), userOf(r).ID, metric, from, to)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	httpx.WriteJSON(w, 200, scoring.TrendSummary(metric, rng, from, to, points, baseline))
}
