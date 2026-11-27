package main

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestDaysServeTrainingLoad(t *testing.T) {
	rr := newRecoveryRig(t)
	ctx := context.Background()
	a, b := rr.user(), rr.user()
	if _, err := rr.pool.Exec(ctx, `UPDATE users SET tz='Europe/Berlin' WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := rr.pool.Exec(ctx, `INSERT INTO daily_scores (user_id, day, trimp, hr_min, muscular_trimp) VALUES
		($1, '2026-09-27', 800, 60, NULL), ($1, '2026-09-28', 16.21, NULL, NULL),
		($1, '2026-10-24', 10.04, 60, 6.15), ($1, '2026-10-25', 33.37, 0, 7.28),
		($1, '2026-10-26', NULL, NULL, NULL), ($2, '2026-10-25', 900, 900, 900),
		($2, '2026-10-27', 900, 900, 900)`, a, b); err != nil {
		t.Fatal(err)
	}
	sessionA := uuid.New()
	if _, err := rr.pool.Exec(ctx, `INSERT INTO sessions (user_id, id, status, started_at) VALUES
		($1, $3, 'completed', '2026-10-24T22:15:00Z'), ($2, $4, 'completed', '2026-10-01T00:00:00Z')`,
		a, b, sessionA, uuid.New()); err != nil {
		t.Fatal(err)
	}

	// Local dates straddle the end of daylight saving time in Berlin.
	midnightWorkout := time.Date(2026, 10, 24, 22, 30, 0, 0, time.UTC)
	curveWorkout := time.Date(2026, 10, 25, 11, 0, 0, 0, time.UTC)
	addWorkout := func(user uuid.UUID, start time.Time, src string, avg *float64, rpe float64) uuid.UUID {
		t.Helper()
		w := model.Workout{ID: uuid.New(), Activity: "running", Start: start, End: start.Add(time.Hour), Source: src, AvgHR: avg}
		if _, err := rr.st.UpsertWorkouts(ctx, user, []model.Workout{w}); err != nil {
			t.Fatal(err)
		}
		if _, err := rr.st.PutWorkoutRating(ctx, user, nil, w.ID, rpe); err != nil {
			t.Fatal(err)
		}
		return w.ID
	}
	noHR := addWorkout(a, midnightWorkout, "phone", nil, 8)
	avgHR := 150.0
	addWorkout(a, curveWorkout.Add(-3*time.Hour), "watch", &avgHR, 10)
	addWorkout(a, curveWorkout, "watch", nil, 10)
	dupe := addWorkout(a, midnightWorkout, "second watch", nil, 10)
	if _, err := rr.pool.Exec(ctx, `UPDATE workouts SET duplicate_of=$2 WHERE user_id=$1 AND id=$3`, a, noHR, dupe); err != nil {
		t.Fatal(err)
	}
	// A phone-only workout on a local date without a score row (Oct 23, 00:30
	// Berlin): it still counts, 60 min at RPE 10 → 261.925, and as a data day.
	addWorkout(a, midnightWorkout.AddDate(0, 0, -2), "phone", nil, 10)
	addWorkout(a, curveWorkout.Add(12*time.Hour+30*time.Minute), "phone", nil, 2.5)
	// A rated Train session's workout without heart rate (Oct 25, 17:00 Berlin):
	// its sets are the muscular part, so the rating adds no cardio TRIMP (D308).
	lift := addWorkout(a, curveWorkout.Add(5*time.Hour), "phone", nil, 10)
	if _, err := rr.pool.Exec(ctx, `UPDATE workouts SET session_id=$2 WHERE user_id=$1 AND id=$3`, a, sessionA, lift); err != nil {
		t.Fatal(err)
	}
	// Sept 27, 23:30 Berlin: inside the padded workout read, before the lookback
	// of the first row (Sept 28): dropped, else 261.9 would move its chronic load.
	addWorkout(a, time.Date(2026, 9, 27, 21, 30, 0, 0, time.UTC), "phone", nil, 10)
	addWorkout(b, midnightWorkout, "watch", nil, 10)

	sample := func(at time.Time, source string) model.Sample {
		return model.Sample{Type: model.SampleHeartRate, Start: at, End: at, Value: 150, Unit: "bpm", Source: source}
	}
	// The competing same-hour samples collapse to one in the first workout.
	// The later workout has two samples, including one exactly at its end.
	if _, err := rr.st.UpsertSamples(ctx, a, []model.Sample{
		sample(midnightWorkout, "watch"), sample(midnightWorkout.Add(time.Minute), "second watch"),
		sample(curveWorkout, "watch"), sample(curveWorkout.Add(time.Hour), "watch"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rr.st.UpsertSamples(ctx, b, []model.Sample{
		sample(midnightWorkout.Add(2*time.Minute), "watch"), sample(midnightWorkout.Add(3*time.Minute), "watch"),
	}); err != nil {
		t.Fatal(err)
	}

	// recovery.checkin off (the seeded default): ratings count nowhere (D298,
	// D299), so cardio is the stored heart-rate TRIMP alone (10.04 + 33.37) and
	// only Oct 24 (hr_min 60) is a data day.
	var off []model.DailyScore
	rr.call(a, http.MethodGet, "/v1/days?from=2026-10-25&to=2026-10-26", nil, 200, &off)
	if len(off) != 2 {
		t.Fatalf("score rows with the flag off: %+v", off)
	}
	for _, row := range off {
		c := row.TrainingLoad.Cardio
		if c.Acute7D != 43.4 || c.DaysWithData != 1 {
			t.Errorf("flag off, %s: cardio %+v, want acute 43.4 over 1 data day (no ratings)", row.Day, c)
		}
	}

	rr.flags(nil, []uuid.UUID{a})
	var rows []model.DailyScore
	rr.call(a, http.MethodGet, "/v1/days?from=2026-10-25&to=2026-10-26", nil, 200, &rows)
	if len(rows) != 2 || rows[0].Day != "2026-10-25" || rows[1].Day != "2026-10-26" {
		t.Fatalf("score rows: %+v", rows)
	}
	part := func(acute, chronic float64, days int) model.LoadPart {
		return model.LoadPart{Acute7D: acute, Chronic28DWeekly: chronic, DaysWithData: days, WindowDays: 28}
	}
	// Cardio acute is 261.925 (Oct 23, unscored) + 10.04 + 33.37 + 115.112… =
	// 420.447; Sept 28 (16.21) contributes only to the first row's chronic window.
	// Data days: Oct 23 (rating), 24 (heart rate), 25 (ratings), and on the
	// second row Oct 26 (the 2.5 rating: no load, but data).
	for i, want := range []model.TrainingLoad{
		{Cardio: part(420.4, 109.2, 3), Muscular: part(13.4, 3.4, 1)},
		{Cardio: part(420.4, 105.1, 4), Muscular: part(13.4, 3.4, 2)},
	} {
		got := rows[i].TrainingLoad
		if got == nil || !reflect.DeepEqual(*got, want) {
			t.Errorf("training load on %s: %+v, want %+v", rows[i].Day, got, want)
		}
	}
	var detail model.DayDetail
	rr.call(a, http.MethodGet, "/v1/days/2026-10-25", nil, 200, &detail)
	if detail.Score.TrainingLoad == nil || !reflect.DeepEqual(detail.Score.TrainingLoad, rows[0].TrainingLoad) {
		t.Errorf("detail load: %+v, want %+v", detail.Score.TrainingLoad, rows[0].TrainingLoad)
	}
	if len(detail.Workouts) != 4 || detail.Workouts[0].ID != noHR {
		t.Errorf("detail workouts: %+v", detail.Workouts)
	}

	var placeholder struct {
		Score map[string]json.RawMessage `json:"score"`
	}
	rr.call(a, http.MethodGet, "/v1/days/2026-10-27", nil, 200, &placeholder)
	if got := string(placeholder.Score["training_load"]); got != "null" {
		t.Errorf("placeholder training_load = %q, want null", got)
	}
}
