package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
)

const dayLayout = "2006-01-02"

// location is the user's timezone; every day boundary the API uses is local to it.
func (h *handler) location(r *http.Request) (*time.Location, error) {
	_, loc, err := h.st.GetUser(r.Context(), h.cfg.UserID)
	return loc, err
}

// dateRange parses ?from=&to= (YYYY-MM-DD) with a default of the last 30 days.
func (h *handler) dateRange(r *http.Request) (string, string, error) {
	loc, err := h.location(r)
	if err != nil {
		return "", "", err
	}
	now := time.Now().In(loc)
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if to == "" {
		to = now.Format(dayLayout)
	}
	if from == "" {
		from = now.AddDate(0, 0, -29).Format(dayLayout)
	}
	for _, d := range []string{from, to} {
		if _, err := time.Parse(dayLayout, d); err != nil {
			return "", "", err
		}
	}
	return from, to, nil
}

func (h *handler) days(w http.ResponseWriter, r *http.Request) {
	from, to, err := h.dateRange(r)
	if err != nil {
		httpx.WriteError(w, 400, "from/to must be YYYY-MM-DD")
		return
	}
	rows, err := h.st.DailyScores(r.Context(), h.cfg.UserID, from, to)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	if rows == nil {
		rows = []model.DailyScore{}
	}
	httpx.WriteJSON(w, 200, rows)
}

// day is the Today / Sleep "Day" view: the score, last night in detail with
// its stage timeline, and the day's workouts.
func (h *handler) day(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	day := r.PathValue("day")
	loc, err := h.location(r)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	d, err := time.ParseInLocation(dayLayout, day, loc)
	if err != nil {
		httpx.WriteError(w, 400, "day must be YYYY-MM-DD")
		return
	}
	scores, err := h.st.DailyScores(ctx, h.cfg.UserID, day, day)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	nights, err := h.st.SleepNights(ctx, h.cfg.UserID, day, day)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	workouts, err := h.st.WorkoutsBetween(ctx, h.cfg.UserID, d, d.AddDate(0, 0, 1))
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	if workouts == nil {
		workouts = []model.Workout{}
	}
	out := model.DayDetail{Workouts: workouts,
		Score: model.DailyScore{Day: day, State: model.StateCalibrating, Details: map[string]any{}}}
	if len(scores) > 0 {
		out.Score = scores[0]
	}
	if len(nights) > 0 {
		if s, ok := scoring.NightSummary(nights[0]); ok {
			from, to := scoring.NightWindow(d)
			segs, err := h.st.SleepEndingBetween(ctx, h.cfg.UserID, from, to)
			if err != nil {
				httpx.WriteError(w, 500, err.Error())
				return
			}
			out.Sleep = &model.SleepDetail{SleepSummary: s, Segments: scoring.Hypnogram(segs)}
		}
	}
	httpx.WriteJSON(w, 200, out)
}

// sleepRanges are the Sleep screen's Week / Month tabs, in days ending at `to`.
var sleepRanges = map[string]int{"week": 7, "month": 30}

func (h *handler) sleepRange(w http.ResponseWriter, r *http.Request) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "week"
	}
	n, ok := sleepRanges[rng]
	if !ok {
		httpx.WriteError(w, 422, "range must be week or month")
		return
	}
	loc, err := h.location(r)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	to, err := endDay(r, loc)
	if err != nil {
		httpx.WriteError(w, 400, err.Error())
		return
	}
	from := to.AddDate(0, 0, -(n - 1)).Format(dayLayout)
	nights, err := h.st.SleepNights(r.Context(), h.cfg.UserID, from, to.Format(dayLayout))
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	httpx.WriteJSON(w, 200, scoring.SleepRangeSummary(rng, from, to.Format(dayLayout), nights))
}

// endDay parses the optional ?to=YYYY-MM-DD that anchors a range, default
// today; the result is local midnight.
func endDay(r *http.Request, loc *time.Location) (time.Time, error) {
	v := r.URL.Query().Get("to")
	if v == "" {
		now := time.Now().In(loc)
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc), nil
	}
	t, err := time.ParseInLocation(dayLayout, v, loc)
	if err != nil {
		return t, fmt.Errorf("to must be YYYY-MM-DD")
	}
	return t, nil
}
