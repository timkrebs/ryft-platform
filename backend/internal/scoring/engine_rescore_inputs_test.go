package scoring

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Integration tests (RYFT_TEST_POSTGRES_URL), continued: each history input,
// the retry, the push, and the bounds of the re-score after a change.

// inOrderDays re-scores exactly these stored days, oldest first (a range
// would add rows for the days between them) and returns the rows.
func inOrderDays(t *testing.T, st *store.Store, engine *Engine, user uuid.UUID, days []string) map[string]string {
	t.Helper()
	if err := engine.ComputeDays(context.Background(), user, days); err != nil {
		t.Fatal(err)
	}
	return rowsJSON(t, st, user, slices.Min(days), slices.Max(days))
}

// Each input the later days are computed from: late data that changes it on
// 09-10 re-scores the days after, and they end up as scoring the range in
// order gives. Leaving rhr, resp_rate, strain, nap_min or the night's columns
// out of the comparison fails its case; that every column is compared at all
// is store.TestHistoryInputsCoverHistory's.
func TestEngineRescoreFollowsEveryInput(t *testing.T) {
	night := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		seed func(*testing.T, *store.Store, uuid.UUID) // before scoring in order
		late func(*testing.T, *store.Store, uuid.UUID) // after
		then func(*testing.T, map[string]model.DailyScore)
	}{
		{name: "a longer night: sleep actual and start, the need through the debt", late: func(t *testing.T, st *store.Store, u uuid.UUID) {
			if _, err := st.UpsertSleep(context.Background(), u, []model.SleepSegment{{Start: night.Add(-3 * time.Hour),
				End: night.Add(-time.Hour), Stage: model.SleepCore, Source: "watch"}}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a workout's heart rate: strain, the next day's need", late: func(t *testing.T, st *store.Store, u uuid.UUID) {
			var hours []time.Duration
			for m := 0; m < 60; m++ {
				hours = append(hours, 17*time.Hour+time.Duration(m)*time.Minute)
			}
			addSamples(t, st, u, "2026-09-10", model.SampleHeartRate, "count/min", 150, hours...)
		}, then: func(t *testing.T, days map[string]model.DailyScore) {
			if days["2026-09-10"].Strain == nil || *days["2026-09-10"].Strain == 0 {
				t.Fatal("09-10 has no strain: the case tests nothing")
			}
		}},
		{name: "respiratory rate", seed: func(t *testing.T, st *store.Store, u uuid.UUID) {
			for d := night.AddDate(0, 0, -9); !d.After(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)); d = d.AddDate(0, 0, 1) {
				addSamples(t, st, u, d.Format(dayLayout), model.SampleRespiratoryRate, "count/min", 14+float64(d.Day()%3), 3*time.Hour)
			}
		}, late: func(t *testing.T, st *store.Store, u uuid.UUID) {
			addSamples(t, st, u, "2026-09-10", model.SampleRespiratoryRate, "count/min", 22, 4*time.Hour)
		}},
		{name: "resting heart rate", late: func(t *testing.T, st *store.Store, u uuid.UUID) {
			addSamples(t, st, u, "2026-09-10", model.SampleRestingHR, "count/min", 70, 20*time.Hour)
		}},
		{name: "a nap: the next day's need", late: func(t *testing.T, st *store.Store, u uuid.UUID) {
			if _, err := st.UpsertSleep(context.Background(), u, []model.SleepSegment{{Start: night.Add(15 * time.Hour),
				End: night.Add(15*time.Hour + 40*time.Minute), Stage: model.SleepCore, Source: "watch"}}); err != nil {
				t.Fatal(err)
			}
		}, then: func(t *testing.T, days map[string]model.DailyScore) {
			if n := days["2026-09-11"].SleepNeedNapMin; n == nil || *n != 40 {
				t.Fatalf("09-11 keeps no nap credit: %v", n)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, engine, user := engineStore(t)
			ctx := context.Background()
			seedNights(t, st, user, true)
			if c.seed != nil {
				c.seed(t, st, user)
			}
			if err := engine.ComputeRange(ctx, user, "2026-09-01", "2026-10-10"); err != nil {
				t.Fatal(err)
			}
			c.late(t, st, user)
			cascade := rescored("history")
			if err := engine.ComputeDay(ctx, user, "2026-09-10"); err != nil {
				t.Fatal(err)
			}
			if rescored("history") == cascade {
				t.Fatal("nothing after 09-10 was scored again")
			}
			got := rowsJSON(t, st, user, "2026-09-01", "2026-10-10")
			if c.then != nil {
				c.then(t, scoresByDay(t, st, user, "2026-09-01", "2026-10-10"))
			}
			sameRows(t, c.name, got, inOrder(t, st, engine, user, "2026-09-01", "2026-10-10"))
		})
	}
}

// An attempt wrote the changed day and failed before the days after it. The
// broker's retry of the same event finds the day unchanged; on a redelivery
// it re-scores the days after anyway and finishes the job. Without the
// redelivery mark it would stop, and the safety net cannot see it (the night
// counts are right, a value is not). A dead letter replayed with scripts/dlq.sh
// carries the same mark (amqpbus TestReplayedLetterIsARedelivery).
func TestEngineRetryFinishesAHalfDoneRescore(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	seedNights(t, st, user, true)
	if err := engine.ComputeRange(ctx, user, "2026-09-01", "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	lateHRV(t, st, user, "2026-09-03")
	if _, err := engine.computeDay(ctx, user, "2026-09-03"); err != nil { // the attempt, cut short
		t.Fatal(err)
	}
	if err := engine.ComputeDay(ctx, user, "2026-09-03"); err != nil { // not marked: stops
		t.Fatal(err)
	}
	if n, err := engine.RescoreBehind(ctx, user, "2026-10-12", 50); err != nil || n != 0 {
		t.Fatalf("the safety net sees counts, not values: %d %v", n, err)
	}
	stale := rowsJSON(t, st, user, "2026-09-01", "2026-10-10")

	cascade := rescored("history")
	if err := engine.ComputeDay(events.WithRedelivery(ctx), user, "2026-09-03"); err != nil {
		t.Fatal(err)
	}
	if n := rescored("history") - cascade; n != 30 {
		t.Fatalf("the redelivery re-scored %v days after 09-03, want 30", n)
	}
	fixed := rowsJSON(t, st, user, "2026-09-01", "2026-10-10")
	want := inOrder(t, st, engine, user, "2026-09-01", "2026-10-10")
	sameRows(t, "after the redelivery", fixed, want)
	if stale["2026-09-10"] == want["2026-09-10"] {
		t.Fatal("the unmarked retry already fixed 09-10: the case tests nothing")
	}
}

// The morning push goes once, for today: a re-score after a change never
// announces, whether it reaches today or not.
func TestEngineRescoreAnnouncesNothing(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	var got []model.ScoredEvent
	engine.OnScored(func(_ context.Context, ev model.ScoredEvent) { got = append(got, ev) })
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seedSpan(t, st, user, today.AddDate(0, 0, -10), 11)
	day := func(k int) string { return today.AddDate(0, 0, k).Format(dayLayout) }

	if err := engine.ComputeDay(ctx, user, day(0)); err != nil { // today first: the push
		t.Fatal(err)
	}
	if err := engine.ComputeRange(ctx, user, day(-10), day(-1)); err != nil { // the history: today again
		t.Fatal(err)
	}
	lateHRV(t, st, user, day(-3))
	if err := engine.ComputeDay(ctx, user, day(-3)); err != nil { // a late sample: today again
		t.Fatal(err)
	}
	if err := engine.ComputeDay(events.WithRedelivery(ctx), user, day(-3)); err != nil { // and its retry
		t.Fatal(err)
	}
	if g := scoresByDay(t, st, user, day(0), day(0))[day(0)]; g.BaselineDays != 10 {
		t.Fatalf("today was not scored again: baseline_days=%d", g.BaselineDays)
	}
	if len(got) != 1 || got[0].Day != day(0) {
		t.Fatalf("announced %+v, want today once", got)
	}
}

// The re-score never goes past the user's today, even where a later row is
// stored (a day scored ahead, another timezone).
func TestEngineRescoreStopsAtToday(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	day := func(k int) string { return today.AddDate(0, 0, k).Format(dayLayout) }
	seedSpan(t, st, user, today.AddDate(0, 0, -10), 10)
	if err := engine.ComputeRange(ctx, user, day(-10), day(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.computeDay(ctx, user, day(2)); err != nil {
		t.Fatal(err)
	}
	ahead := scoresByDay(t, st, user, day(2), day(2))[day(2)].ComputedAt
	lateHRV(t, st, user, day(-5))
	cascade := rescored("history")
	if err := engine.ComputeDay(ctx, user, day(-5)); err != nil {
		t.Fatal(err)
	}
	if n := rescored("history") - cascade; n != 5 {
		t.Fatalf("re-scored %v days, want 5 (the 4 after it and today)", n)
	}
	if got := scoresByDay(t, st, user, day(2), day(2))[day(2)].ComputedAt; !got.Equal(ahead) {
		t.Fatal("a day after today was scored again")
	}
}

// One call re-scores at most maxAgain days beyond the ones asked for and says
// so in the log.
func TestEngineRescoreCapped(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	seedNights(t, st, user, true)
	if err := engine.ComputeRange(ctx, user, "2026-09-01", "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	engine.log = slog.New(slog.NewTextHandler(&logs, nil))
	engine.maxAgain = 5
	lateHRV(t, st, user, "2026-09-03")
	cascade := rescored("history")
	if err := engine.ComputeDay(ctx, user, "2026-09-03"); err != nil {
		t.Fatal(err)
	}
	if n := rescored("history") - cascade; n != 5 {
		t.Fatalf("re-scored %v days, want the cap (5)", n)
	}
	if !strings.Contains(logs.String(), "rescore after history change capped") {
		t.Fatalf("no capped line in the log:\n%s", logs.String())
	}
}

// Stored days months apart: the re-score follows the 30 stored days after
// the change, as ScoreHistory reads them, not 30 calendar days; and the safety
// net, whose window a gap cuts short, picks nothing that is right.
func TestEngineRescoreAcrossACalendarGap(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	seedSpan(t, st, user, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), 20)
	seedSpan(t, st, user, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), 10)
	if err := engine.ComputeRange(ctx, user, "2026-05-01", "2026-05-20"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeRange(ctx, user, "2026-09-01", "2026-09-10"); err != nil {
		t.Fatal(err)
	}
	if g := scoresByDay(t, st, user, "2026-09-01", "2026-09-01")["2026-09-01"]; g.BaselineDays != 20 {
		t.Fatalf("09-01 reads May's nights: baseline_days=%d", g.BaselineDays)
	}
	lateHRV(t, st, user, "2026-05-15")
	cascade := rescored("history")
	if err := engine.ComputeDay(ctx, user, "2026-05-15"); err != nil {
		t.Fatal(err)
	}
	if n := rescored("history") - cascade; n != 5+10 {
		t.Fatalf("re-scored %v days, want 15 (05-16 .. 05-20, 09-01 .. 09-10)", n)
	}
	var stored []string
	for d := range scoresByDay(t, st, user, "2026-05-01", "2026-09-10") {
		stored = append(stored, d)
	}
	if len(stored) != 30 {
		t.Fatalf("stored %d days", len(stored))
	}
	got := rowsJSON(t, st, user, "2026-05-01", "2026-09-10")
	sameRows(t, "across the gap", got, inOrderDays(t, st, engine, user, stored))
	if b, err := st.BehindHistoryDays(ctx, user, "2026-09-01", baselineDays, 50); err != nil || len(b) != 0 {
		t.Fatalf("behind after a gap: %v %v", b, err)
	}
}

// An account without HRV keeps baseline_days 0, so the counts never show a
// day scored before its history; its missing resting-HR baseline does.
func TestEngineRescoreBehindMissingBaselines(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	seedNights(t, st, user, false)
	order := []string{"2026-10-10"}
	for d := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); d.Before(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)); d = d.AddDate(0, 0, 1) {
		order = append(order, d.Format(dayLayout))
	}
	for _, d := range order {
		if _, err := engine.computeDay(ctx, user, d); err != nil {
			t.Fatal(err)
		}
	}
	if g := scoresByDay(t, st, user, "2026-10-10", "2026-10-10")["2026-10-10"]; g.RHRBaseline != nil {
		t.Fatal("10-10 was scored first, without a baseline")
	}
	behind, err := st.BehindHistoryDays(ctx, user, "2026-09-01", baselineDays, 50)
	if err != nil || !slices.Equal(behind, []string{"2026-10-10"}) {
		t.Fatalf("behind: %v %v", behind, err)
	}
	if n, err := engine.RescoreBehind(ctx, user, "2026-10-12", 50); err != nil || n != 1 {
		t.Fatalf("rescore behind: %d %v", n, err)
	}
	g := scoresByDay(t, st, user, "2026-10-10", "2026-10-10")["2026-10-10"]
	if g.RHRBaseline == nil || g.BaselineDays != 0 || !g.Calibrating {
		t.Fatalf("10-10 after the pass: rhr baseline %v, baseline_days %d, calibrating %v", g.RHRBaseline, g.BaselineDays, g.Calibrating)
	}
	if n, err := engine.RescoreBehind(ctx, user, "2026-10-12", 50); err != nil || n != 0 {
		t.Fatalf("the next pass: %d %v", n, err)
	}
}
