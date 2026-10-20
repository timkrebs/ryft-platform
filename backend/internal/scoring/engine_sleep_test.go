package scoring

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

func scoresByDay(t *testing.T, st *store.Store, user uuid.UUID, from, to string) map[string]model.DailyScore {
	t.Helper()
	rows, err := st.DailyScores(context.Background(), user, from, to)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]model.DailyScore{}
	for _, r := range rows {
		out[r.Day] = r
	}
	return out
}

// Three weeks of nights alternating 23:00–07:00 and 23:30–07:30 (UTC), and a
// 40-minute nap on the 20th: the nap is the 20th's nap_min and the 21st's
// credit, the need adds up, consistency comes with the 5th night.
func TestEngineSleepNapConsistencyQuality(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	first := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var segs []model.SleepSegment
	for k := 0; k < 21; k++ {
		d := first.AddDate(0, 0, k)
		off := time.Duration(k%2*30) * time.Minute
		segs = append(segs, model.SleepSegment{Start: d.Add(-time.Hour + off), End: d.Add(7*time.Hour + off),
			Stage: model.SleepCore, Source: "watch"})
	}
	nap := first.AddDate(0, 0, 19).Add(15 * time.Hour)
	segs = append(segs, model.SleepSegment{Start: nap, End: nap.Add(40 * time.Minute), Stage: model.SleepCore, Source: "watch"})
	if _, err := st.UpsertSleep(ctx, user, segs); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeRange(ctx, user, "2026-10-01", "2026-10-21"); err != nil {
		t.Fatal(err)
	}
	days := scoresByDay(t, st, user, "2026-10-01", "2026-10-21")
	d20, d21 := days["2026-10-20"], days["2026-10-21"]
	if d20.NapMin == nil || *d20.NapMin != 40 || *d20.SleepNeedNapMin != 0 {
		t.Fatalf("the 20th: nap %v credit %v", d20.NapMin, d20.SleepNeedNapMin)
	}
	if *d21.SleepNeedNapMin != 40 || *d21.NapMin != 0 {
		t.Fatalf("the 21st: credit %v nap %v", d21.SleepNeedNapMin, d21.NapMin)
	}
	for _, d := range []model.DailyScore{d20, d21} {
		if *d.SleepNeedMin != *d.SleepNeedBaseMin+*d.SleepNeedStrainMin+*d.SleepNeedDebtMin-*d.SleepNeedNapMin {
			t.Fatalf("%s: the need must add up: %+v", d.Day, d)
		}
		if *d.SleepActualMin != 480 {
			t.Fatalf("%s: the nap is not part of the night: %d", d.Day, *d.SleepActualMin)
		}
	}
	if *d21.SleepNeedMin >= *d20.SleepNeedMin {
		t.Fatalf("the credit lowers the need: %d vs %d", *d21.SleepNeedMin, *d20.SleepNeedMin)
	}
	// σ of 7 nights alternating by 30 min ≈ 14.85 for bed and for wake → 85.
	if c := days["2026-10-04"].SleepConsistency; c != nil {
		t.Fatalf("4 nights: no consistency yet, got %d", *c)
	}
	if c := d21.SleepConsistency; c == nil || *c != 85 {
		t.Fatalf("consistency %v", c)
	}
	if q := d21.SleepQuality; q == nil || *q == *d21.SleepPerformance {
		t.Fatalf("quality %v must be its own score (performance %d)", q, *d21.SleepPerformance)
	}
	for _, d := range days {
		if d.ModelVersion == nil || *d.ModelVersion != ModelVersion {
			t.Fatalf("%s: model_version %v, want %s", d.Day, d.ModelVersion, ModelVersion)
		}
	}
}

// Learning: 20 good nights of 500 minutes after easy days. The default user
// takes the estimate and the last 7 days are rescored with it; a user who set
// the baseline by hand keeps it and the estimate is only stored.
func TestEngineLearnsSleepBase(t *testing.T) {
	st, engine, auto := engineStore(t)
	ctx := context.Background()
	manual := uuid.New()
	if _, err := st.EnsureUser(ctx, store.Identity{ID: manual}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUser(ctx, manual, model.UserPatch{TZ: ptrTo("UTC"), SleepBaseMin: ptrTo(450)}); err != nil {
		t.Fatal(err)
	}
	strain, recovery, asleep := 5.0, 80, 500
	for _, id := range []uuid.UUID{auto, manual} {
		for k := 0; k < 20; k++ {
			day := time.Date(2026, 10, 2+k, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
			row := model.DailyScore{Day: day, Strain: &strain, Recovery: &recovery, SleepActualMin: &asleep}
			if _, err := st.UpsertDailyScore(ctx, id, store.ScoreRow{DailyScore: row}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if out, err := engine.LearnBaseline(ctx, manual, "2026-10-21"); err != nil || out != LearnKept {
		t.Fatalf("manual: %s %v", out, err)
	}
	if u, _, _ := st.GetUser(ctx, manual); u.SleepBaseMin != 450 || u.SleepBaseSource != model.BaseManual || *u.SleepBaseLearnedMin != 500 {
		t.Fatalf("manual wins: %+v", u)
	}
	if d := scoresByDay(t, st, manual, "2026-10-21", "2026-10-21")["2026-10-21"]; d.SleepActualMin == nil {
		t.Fatal("manual: nothing rescored")
	}

	if out, err := engine.LearnBaseline(ctx, auto, "2026-10-21"); err != nil || out != LearnApplied {
		t.Fatalf("auto: %s %v", out, err)
	}
	if u, _, _ := st.GetUser(ctx, auto); u.SleepBaseMin != 500 || u.SleepBaseSource != model.BaseLearned || u.SleepBaseLearnedAt == nil {
		t.Fatalf("learned: %+v", u)
	}
	days := scoresByDay(t, st, auto, "2026-10-14", "2026-10-21")
	if d := days["2026-10-15"]; d.SleepNeedBaseMin == nil || *d.SleepNeedBaseMin != 500 {
		t.Fatalf("the last 7 days are rescored with the new base: %+v", d)
	}
	if d := days["2026-10-14"]; d.SleepNeedBaseMin != nil {
		t.Fatalf("older days are left alone: %+v", d)
	}
	if out, _ := engine.LearnBaseline(ctx, auto, "2026-10-21"); out != LearnInsufficient {
		// The rescore replaced the 7 fabricated nights with real (empty) ones: 13 nights left.
		t.Fatalf("after the rescore: %s", out)
	}
}

// The morning hook: today's first recovery after a night with sleep calls it
// once; a rescore of today, and past days, never.
func TestEngineAnnouncesTodaysFirstRecovery(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	var got []model.ScoredEvent
	engine.OnScored(func(_ context.Context, ev model.ScoredEvent) { got = append(got, ev) })
	today := time.Now().UTC().Truncate(24 * time.Hour)
	var segs []model.SleepSegment
	for k := 3; k >= 0; k-- {
		d := today.AddDate(0, 0, -k)
		segs = append(segs, model.SleepSegment{Start: d.Add(-time.Hour), End: d.Add(6 * time.Hour), Stage: model.SleepCore, Source: "watch"})
	}
	if _, err := st.UpsertSleep(ctx, user, segs); err != nil {
		t.Fatal(err)
	}
	from, to := today.AddDate(0, 0, -3).Format("2006-01-02"), today.Format("2006-01-02")
	if err := engine.ComputeRange(ctx, user, from, to); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Day != to || got[0].SleepMin != 420 || got[0].Recovery <= 0 {
		t.Fatalf("one event, for today: %+v", got)
	}
	if err := engine.ComputeRange(ctx, user, from, to); err != nil || len(got) != 1 {
		t.Fatalf("a rescore announces nothing: %d events", len(got))
	}
}
