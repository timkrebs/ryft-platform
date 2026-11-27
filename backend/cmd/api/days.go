package main

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
)

const dayLayout = "2006-01-02"

// userOf is the authenticated caller's user row (loaded by the auth middleware).
func userOf(r *http.Request) model.User {
	u, _ := auth.UserFrom(r.Context())
	return u
}

// location is the caller's timezone; every day boundary the API uses is local to it.
func (h *handler) location(r *http.Request) (*time.Location, error) {
	return loadLocation(userOf(r).TZ)
}

// locations caches parsed timezones; there are only a few distinct ones.
var locations sync.Map

func loadLocation(name string) (*time.Location, error) {
	if l, ok := locations.Load(name); ok {
		return l.(*time.Location), nil
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("user tz %q: %w", name, err)
	}
	locations.Store(name, l)
	return l, nil
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
			return "", "", errBadDate
		}
	}
	if from > to {
		return "", "", errFromAfterTo
	}
	return from, to, nil
}

var (
	errBadDate     = errors.New("from/to must be YYYY-MM-DD")
	errFromAfterTo = errors.New("from must be <= to")
)

// writeRangeError maps a dateRange error to its status: 422 for an inverted
// range, 400 for a malformed date, 500 for anything else (the user lookup).
func writeRangeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errFromAfterTo):
		httpx.WriteError(w, 422, err.Error())
	case errors.Is(err, errBadDate):
		httpx.WriteError(w, 400, err.Error())
	default:
		httpx.Fail(w, r, err)
	}
}

func (h *handler) days(w http.ResponseWriter, r *http.Request) {
	from, to, err := h.dateRange(r)
	if err != nil {
		writeRangeError(w, r, err)
		return
	}
	rows, err := h.st.DailyScores(r.Context(), userOf(r).ID, from, to)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if rows == nil {
		rows = []model.DailyScore{}
	}
	today, loc, err := h.localToday(r)
	if err == nil {
		err = h.withScoringStates(r.Context(), userOf(r).ID, today, rows)
	}
	if err == nil {
		err = h.withTrainingLoad(r, loc, rows)
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, rows)
}

// withTrainingLoad fills scored rows (oldest first, as DailyScores returns them)
// from one shared lookback: the 27 days before the first row through the last
// (docs/API.md → Training load; SCORING.md → Training load (Fitness); D308).
// The span follows the rows, not the request, so a wide ?from= reads no more
// than the scored days need. A fixed number of queries per request: the day
// values, the flag, the rated workouts, one batched heart-rate read for those
// without avg_hr, and the first completed session.
func (h *handler) withTrainingLoad(r *http.Request, loc *time.Location, rows []model.DailyScore) error {
	if len(rows) == 0 {
		return nil
	}
	ctx, userID := r.Context(), userOf(r).ID
	// Stored days are valid dates. Step in UTC before turning the bounds into
	// local midnights, independent of the length of a local day.
	first, err := time.Parse(dayLayout, rows[0].Day)
	if err != nil {
		return fmt.Errorf("training load first day: %w", err)
	}
	last, err := time.Parse(dayLayout, rows[len(rows)-1].Day)
	if err != nil {
		return fmt.Errorf("training load last day: %w", err)
	}
	to := last.Format(dayLayout)
	first = first.AddDate(0, 0, 1-scoring.TrainingLoadWindowDays)
	firstDay := first.Format(dayLayout)
	// The workout read is padded by a day on each side, so no timezone or DST
	// transition can cut off an edge date; the local date key decides below.
	start := time.Date(first.Year(), first.Month(), first.Day()-1, 0, 0, 0, 0, loc)
	end := time.Date(last.Year(), last.Month(), last.Day()+2, 0, 0, 0, 0, loc)
	stored, err := h.st.TrainingLoadDays(ctx, userID, firstDay, to)
	if err != nil {
		return fmt.Errorf("read training load days: %w", err)
	}
	// Ratings count only while recovery.checkin is on for the account: the flag
	// is the kill switch for counting them anywhere (D298, D299), so with it off
	// the load is heart rate and sessions alone.
	ratingsOn, err := h.st.RecoveryEnabled(ctx, userID)
	if err != nil {
		return fmt.Errorf("read training load rating flag: %w", err)
	}
	var workouts []model.Workout
	if ratingsOn {
		if workouts, err = h.st.RatedWorkoutsBetween(ctx, userID, start, end); err != nil {
			return fmt.Errorf("read training load workouts: %w", err)
		}
	}
	firstSession, err := h.st.FirstCompletedSessionStart(ctx, userID)
	if err != nil {
		return fmt.Errorf("read first completed session: %w", err)
	}
	firstSessionDay := ""
	if firstSession != nil {
		firstSessionDay = firstSession.In(loc).Format(dayLayout)
	}
	inputs := make(map[string]scoring.TrainingLoadDay, len(stored))
	for _, d := range stored {
		inputs[d.Day] = scoring.TrainingLoadDay{
			CardioTRIMP: d.TRIMP, HasHR: d.HRMin > 0, MuscularTRIMP: d.MuscularTRIMP,
		}
	}
	// The rated workouts that count, and among them those whose heart rate must be
	// looked at (no avg_hr): their sample windows are read in one query.
	var rated, noAvg []model.Workout
	var windows []store.SampleWindow
	for _, workout := range workouts {
		if workout.SessionRPE == nil || !scoring.ValidWorkoutRPE(*workout.SessionRPE) {
			continue
		}
		// A Train session's workout carries its load in the muscular part (its
		// sets); a rating on it must not add cardio too (D308).
		if workout.SessionID != nil || workout.Source == model.SessionSource {
			continue
		}
		if day := workout.Start.In(loc).Format(dayLayout); day < firstDay || day > to {
			continue
		}
		rated = append(rated, workout)
		if workout.AvgHR == nil {
			from, end := scoring.SampleWindow(workout.Start, scoring.WorkoutWindowEnd(workout))
			noAvg, windows = append(noAvg, workout), append(windows, store.SampleWindow{From: from, To: end})
		}
	}
	around, err := h.st.SamplesInWindows(ctx, userID, model.SampleHeartRate, windows)
	if err != nil {
		return fmt.Errorf("read training load heart-rate samples: %w", err)
	}
	noHR := make(map[uuid.UUID]bool, len(noAvg))
	for i, workout := range noAvg {
		noHR[workout.ID] = len(scoring.WorkoutSamples(workout, around[i])) < 2
	}
	for _, workout := range rated {
		// A rated workout counts on its local date whether or not the date has a
		// score row: a phone-only day is exactly the case the rating is for.
		day := workout.Start.In(loc).Format(dayLayout)
		input := inputs[day]
		input.HasRating = true
		if noHR[workout.ID] {
			input.RatedTRIMP += scoring.RatedWorkoutTRIMP(workout.End.Sub(workout.Start).Minutes(), *workout.SessionRPE)
		}
		inputs[day] = input
	}
	for i := range rows {
		load := scoring.TrainingLoadOn(rows[i].Day, inputs, firstSessionDay)
		rows[i].TrainingLoad = &load
	}
	return nil
}

// day is the Today / Sleep "Day" / Strain view: the score, last night in
// detail with its stage timeline, the day's heart-rate load and its workouts.
func (h *handler) day(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	day := r.PathValue("day")
	loc, err := h.location(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	d, err := time.ParseInLocation(dayLayout, day, loc)
	if err != nil {
		httpx.WriteError(w, 400, "day must be YYYY-MM-DD")
		return
	}
	scores, err := h.st.DailyScores(ctx, userOf(r).ID, day, day)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	nights, err := h.st.SleepNights(ctx, userOf(r).ID, day, day)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	workouts, err := h.st.RatedWorkoutsBetween(ctx, userOf(r).ID, d, d.AddDate(0, 0, 1))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if workouts == nil {
		workouts = []model.Workout{}
	}
	out := model.DayDetail{Workouts: workouts,
		Score: model.DailyScore{Day: day, State: model.StateCalibrating, Details: map[string]any{}}}
	if len(scores) > 0 {
		if err := h.withTrainingLoad(r, loc, scores); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		out.Score = scores[0]
		load, err := h.st.DayLoadOf(ctx, userOf(r).ID, day)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		out.Strain = scoring.DayStrainFrom(load)
		if out.Strain != nil {
			sessions, err := h.st.SessionSummaries(ctx, userOf(r).ID, d, d.AddDate(0, 0, 1), model.SessionCompleted, true)
			if err != nil {
				httpx.Fail(w, r, err)
				return
			}
			scoring.DayStrainSessions(out.Strain, sessions)
		}
	}
	if len(nights) > 0 {
		if s, ok := scoring.NightSummary(nights[0]); ok {
			// The night as scoring took it: one source, morning naps split off.
			from, to := scoring.DaySleepWindow(d)
			segs, err := h.st.SleepEndingBetween(ctx, userOf(r).ID, from, to)
			if err != nil {
				httpx.Fail(w, r, err)
				return
			}
			night, _ := scoring.SplitDay(segs, d)
			out.Sleep = &model.SleepDetail{SleepSummary: s, Need: scoring.NeedOf(out.Score), Segments: scoring.Hypnogram(night)}
		}
	}
	today := time.Now().In(loc).Format(dayLayout)
	states := []model.DailyScore{out.Score}
	if err := h.withScoringStates(ctx, userOf(r).ID, today, states); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out.Score.ScoringState = states[0].ScoringState
	httpx.WriteJSON(w, 200, out)
}

// sleepPlan is the Sleep Planner: bedtimes for a wake time, from tonight's need.
func (h *handler) sleepPlan(w http.ResponseWriter, r *http.Request) {
	loc, err := h.location(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	now := time.Now().In(loc)
	wake, err := scoring.NextWake(r.URL.Query().Get("wake"), now)
	if err != nil {
		httpx.WriteError(w, 422, err.Error())
		return
	}
	today := now.Format(dayLayout)
	tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc).Format(dayLayout)
	hist, err := h.st.ScoreHistory(r.Context(), userOf(r).ID, tomorrow, 7)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, scoring.TonightPlan(userOf(r).SleepBaseMin, today, hist, wake))
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
		httpx.Fail(w, r, err)
		return
	}
	to, err := endDay(r, loc)
	if err != nil {
		httpx.WriteError(w, 400, err.Error())
		return
	}
	from := to.AddDate(0, 0, -(n - 1)).Format(dayLayout)
	nights, err := h.st.SleepNights(r.Context(), userOf(r).ID, from, to.Format(dayLayout))
	if err != nil {
		httpx.Fail(w, r, err)
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
