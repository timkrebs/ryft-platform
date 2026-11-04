package main

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
)

func (h *handler) workouts(w http.ResponseWriter, r *http.Request) {
	from, to, err := h.dateRange(r)
	if err != nil {
		writeRangeError(w, r, err)
		return
	}
	hidden := false
	switch v := r.URL.Query().Get("include_hidden"); v {
	case "", "false":
	case "true":
		hidden = true
	default:
		httpx.WriteError(w, 422, "include_hidden must be true or false")
		return
	}
	loc, err := h.location(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a, _ := time.ParseInLocation(dayLayout, from, loc)
	b, _ := time.ParseInLocation(dayLayout, to, loc)
	rows, err := h.st.ListWorkoutsBetween(r.Context(), userOf(r).ID, a, b.AddDate(0, 0, 1), hidden)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if rows == nil {
		rows = []model.Workout{}
	}
	httpx.WriteJSON(w, 200, rows)
}

// workout is the workout screen: the row, its heart-rate curve and the zone
// minutes / TRIMP scoring stored on it.
func (h *handler) workout(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, 400, "id must be a UUID")
		return
	}
	row, err := h.st.Workout(r.Context(), userOf(r).ID, id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, 404, "workout not found")
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	from, to := scoring.SampleWindow(row.Start, scoring.WorkoutWindowEnd(row.Workout))
	hr, err := h.st.SamplesBetween(r.Context(), userOf(r).ID, model.SampleHeartRate, from, to)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, scoring.WorkoutDetail(row, scoring.WorkoutSamples(row.Workout, hr)))
}
