package store

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestTrainingLoadReadsAreUserScoped(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{a, b} {
		if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO daily_scores (user_id, day, trimp, hr_min, muscular_trimp) VALUES
		($1, '2026-11-01', 11.25, 15, 23.75), ($1, '2026-11-02', NULL, NULL, NULL),
		($1, '2026-11-03', 200, 200, 200), ($2, '2026-11-01', 900, 900, 900)`, a, b); err != nil {
		t.Fatal(err)
	}
	rows, err := st.TrainingLoadDays(ctx, a, "2026-11-01", "2026-11-02")
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]TrainingLoadDay)
	for _, row := range rows {
		got[row.Day] = row
	}
	want := map[string]TrainingLoadDay{
		"2026-11-01": {Day: "2026-11-01", TRIMP: 11.25, HRMin: 15, MuscularTRIMP: 23.75},
		"2026-11-02": {Day: "2026-11-02"},
	}
	if len(rows) != len(want) || !maps.Equal(got, want) {
		t.Fatalf("load rows: %+v, want %+v", rows, want)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO sessions (user_id, id, status, started_at) VALUES
		($1, $3, 'discarded', '2026-10-15T00:00:00Z'),
		($1, $4, 'in_progress', '2026-10-20T00:00:00Z'),
		($2, $5, 'completed', '2026-10-01T00:00:00Z')`, a, b, uuid.New(), uuid.New(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	first, err := st.FirstCompletedSessionStart(ctx, a)
	if err != nil || first != nil {
		t.Fatalf("without a completed session: %v, %v", first, err)
	}
	start := time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO sessions (user_id, id, status, started_at) VALUES
		($1, $2, 'completed', $3), ($1, $4, 'completed', $5)`,
		a, uuid.New(), start.AddDate(0, 0, 1), uuid.New(), start); err != nil {
		t.Fatal(err)
	}
	first, err = st.FirstCompletedSessionStart(ctx, a)
	if err != nil || first == nil || !first.Equal(start) {
		t.Fatalf("first completed session: %v, %v, want %v", first, err, start)
	}
}

// SamplesInWindows answers per window, in order, the user's samples of the type
// only, [From, To) like SamplesBetween; a sample in two windows is in both.
func TestSamplesInWindowsIsPerWindowAndUserScoped(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{a, b} {
		if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	hr := func(at time.Time) model.Sample {
		return model.Sample{Type: model.SampleHeartRate, Start: at, End: at, Value: 120, Unit: "bpm", Source: "watch"}
	}
	if _, err := st.UpsertSamples(ctx, a, []model.Sample{
		hr(t0), hr(t0.Add(30 * time.Minute)), hr(t0.Add(time.Hour)), hr(t0.Add(2 * time.Hour)),
		{Type: model.SampleSteps, Start: t0, End: t0.Add(time.Minute), Value: 50, Unit: "count", Source: "watch"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSamples(ctx, b, []model.Sample{hr(t0.Add(10 * time.Minute))}); err != nil {
		t.Fatal(err)
	}
	// 10:00 and 10:30 (11:00 is the open end); 10:30 and 11:00, overlapping the
	// first; none.
	got, err := st.SamplesInWindows(ctx, a, model.SampleHeartRate, []SampleWindow{
		{From: t0, To: t0.Add(time.Hour)},
		{From: t0.Add(30 * time.Minute), To: t0.Add(2 * time.Hour)},
		{From: t0.Add(3 * time.Hour), To: t0.Add(4 * time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]time.Time{{t0, t0.Add(30 * time.Minute)}, {t0.Add(30 * time.Minute), t0.Add(time.Hour)}, nil}
	if len(got) != len(want) {
		t.Fatalf("windows: %d, want %d", len(got), len(want))
	}
	for i := range want {
		var starts []time.Time
		for _, s := range got[i] {
			if s.Type != model.SampleHeartRate {
				t.Errorf("window %d: a %s sample", i, s.Type)
			}
			starts = append(starts, s.Start.UTC())
		}
		if !slices.EqualFunc(starts, want[i], time.Time.Equal) {
			t.Errorf("window %d: %v, want %v (b's 10:10 sample must not appear)", i, starts, want[i])
		}
	}
	if empty, err := st.SamplesInWindows(ctx, a, model.SampleHeartRate, nil); err != nil || len(empty) != 0 {
		t.Errorf("no windows: %v, %v", empty, err)
	}
}
