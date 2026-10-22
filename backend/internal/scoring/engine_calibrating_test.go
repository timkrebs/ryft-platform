package scoring

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// seedNights writes 40 nights (2026-09-01 .. 2026-10-10) of sleep, a resting
// heart rate and, when withHRV, an overnight HRV sample.
func seedNights(t *testing.T, st *store.Store, user uuid.UUID, withHRV bool) {
	t.Helper()
	ctx := context.Background()
	first := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var segs []model.SleepSegment
	var samples []model.Sample
	for k := 0; k < 40; k++ {
		d := first.AddDate(0, 0, k)
		segs = append(segs, model.SleepSegment{Start: d.Add(-time.Hour), End: d.Add(7 * time.Hour),
			Stage: model.SleepCore, Source: "watch"})
		samples = append(samples, model.Sample{Type: model.SampleRestingHR, Start: d.Add(9 * time.Hour),
			End: d.Add(9 * time.Hour), Value: 55 + float64(k%3), Unit: "count/min", Source: "watch"})
		if withHRV {
			samples = append(samples, model.Sample{Type: model.SampleHRVSDNN, Start: d.Add(3 * time.Hour),
				End: d.Add(3 * time.Hour), Value: 60 + float64(k%5), Unit: "ms", Source: "watch"})
		}
	}
	if _, err := st.UpsertSleep(ctx, user, segs); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSamples(ctx, user, samples); err != nil {
		t.Fatal(err)
	}
}

// The newest day is scored first (a backfill that arrives newest-chunk-first,
// a recompute range replayed out of order, a second consumer during a rollout
// surge), then the days before it. The newest day was scored against an empty
// history and nothing re-scores it, so it keeps calibrating with 0 baseline
// days although 30 nights with HRV and resting heart rate now sit behind it.
func TestEngineLateHistoryRescoresTheDaysAfterIt(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	seedNights(t, st, user, true)

	if err := engine.ComputeDay(ctx, user, "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeRange(ctx, user, "2026-09-01", "2026-10-09"); err != nil {
		t.Fatal(err)
	}
	got := scoresByDay(t, st, user, "2026-10-10", "2026-10-10")["2026-10-10"]
	if got.BaselineDays != 30 || got.Calibrating {
		t.Fatalf("2026-10-10 was scored before its history: baseline_days=%d calibrating=%v (want 30, false)",
			got.BaselineDays, got.Calibrating)
	}
}

// The oldest-first range is the control: the same data scored in order.
func TestEngineInOrderRangeHasBaselines(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	seedNights(t, st, user, true)
	if err := engine.ComputeRange(ctx, user, "2026-09-01", "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	days := scoresByDay(t, st, user, "2026-09-01", "2026-10-10")
	if g := days["2026-10-10"]; g.BaselineDays != 30 || g.Calibrating {
		t.Fatalf("in order: baseline_days=%d calibrating=%v", g.BaselineDays, g.Calibrating)
	}
	if g := days["2026-09-05"]; g.BaselineDays != 4 || !g.Calibrating {
		t.Fatalf("fifth day: baseline_days=%d calibrating=%v", g.BaselineDays, g.Calibrating)
	}
}

// A source that writes resting heart rate and sleep but never HRV (case 2 of
// the report): the day is scored, with a recovery, and stays calibrating with
// 0 baseline days for ever, however many nights come. That is today's
// behaviour, pinned; the app needs calibrating_reason to say "no HRV source".
func TestEngineNoHRVStaysCalibratingWithARecovery(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	seedNights(t, st, user, false)
	if err := engine.ComputeRange(ctx, user, "2026-09-01", "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	g := scoresByDay(t, st, user, "2026-10-10", "2026-10-10")["2026-10-10"]
	if g.BaselineDays != 0 || !g.Calibrating {
		t.Fatalf("no HRV: baseline_days=%d calibrating=%v", g.BaselineDays, g.Calibrating)
	}
	if g.Recovery == nil || *g.Recovery == 0 {
		t.Fatalf("a scored day with sleep and resting HR has a recovery, got %v", g.Recovery)
	}
}
