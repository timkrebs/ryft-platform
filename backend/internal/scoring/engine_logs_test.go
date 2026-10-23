package scoring

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/logx/logcheck"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// engineLogKeys is every line the engine logs with the keys it may carry
// (besides logLineKeys): what was scored, never a score, an input or a state
// (docs/OPERATIONS.md → Data protection (logs)). Any other key fails, so a score
// under a new name (recovery as "score") cannot slip through; a new line is
// added here once it is checked against that rule.
//
// Each key also has a kind of value (days are days, counts are integers within
// the engine's bound, flags are booleans): a score logged under an allowed key
// fails on its type. An integer under a count key is not caught by type alone.
var engineLogKeys = logcheck.Lines{
	"scored day": {"day": logcheck.Day, "model_version": logcheck.Enum(ModelVersion),
		"inputs_changed": logcheck.Bool},
	"rescored days after a history change": {"days": logcheck.CountMax(MaxRangeDays), "through": logcheck.Day},
	"rescored days behind their history":   {"days": logcheck.CountMax(MaxRangeDays), "from": logcheck.Day},
	"rescore after history change capped":  {"after": logcheck.Day, "days": logcheck.CountMax(MaxRangeDays)},
	"mark scored":                          {"day": logcheck.Day, "err": logcheck.Text(1024)},
	"sleep baseline learned":               {"applied": logcheck.Bool},
}

// logLineKeys are on every line: logx's and the context's.
var logLineKeys = logcheck.Standard("scoring")

// A scoring run at debug level — ten days in order, a late night that re-scores
// the days after it, a learned sleep baseline — logs which days it scored and
// nothing of the person's data: only the keys above, and none of the seeded
// values (HRV 61.7 ms, resting HR 47.3, 14.7 breaths a minute, 517 minutes
// asleep, 12345 steps) under any key.
func TestEngineLogsNoHealthValues(t *testing.T) {
	st, _, user := engineStore(t)
	var buf bytes.Buffer
	engine := NewEngine(st, logx.NewWriter(&buf, "scoring", slog.LevelDebug))
	requestID := uuid.NewString() // httpx makes them UUIDs; the kinds pin that
	ctx := logx.WithFields(context.Background(), &logx.Fields{RequestID: requestID, UserID: user.String()})

	first := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	night := func(d time.Time) ([]model.SleepSegment, []model.Sample) {
		sample := func(typ string, at time.Duration, v float64, unit string) model.Sample {
			return model.Sample{Type: typ, Start: d.Add(at), End: d.Add(at), Value: v, Unit: unit, Source: "watch"}
		}
		return []model.SleepSegment{{Start: d.Add(-77 * time.Minute), End: d.Add(440 * time.Minute), // 517 min
				Stage: model.SleepCore, Source: "watch"}},
			[]model.Sample{sample(model.SampleHRVSDNN, 3*time.Hour, 61.7, "ms"),
				sample(model.SampleRespiratoryRate, 3*time.Hour, 14.7, "count/min"),
				sample(model.SampleRestingHR, 9*time.Hour, 47.3, "count/min"),
				sample(model.SampleSteps, 12*time.Hour, 12345, "count")}
	}
	seed := func(d time.Time) {
		segs, samples := night(d)
		if _, err := st.UpsertSleep(ctx, user, segs); err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpsertSamples(ctx, user, samples); err != nil {
			t.Fatal(err)
		}
	}
	for k := 1; k <= 10; k++ {
		seed(first.AddDate(0, 0, k))
	}
	if err := engine.ComputeRange(ctx, user, "2026-10-01", "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	// A night that arrives late: the days after it are scored again.
	seed(first)
	if err := engine.ComputeDay(ctx, user, "2026-09-30"); err != nil {
		t.Fatal(err)
	}
	last := scoresByDay(t, st, user, "2026-10-10", "2026-10-10")["2026-10-10"]
	if last.HRVms == nil || *last.HRVms != 61.7 || last.RHR == nil || *last.RHR != 47.3 ||
		last.SleepActualMin == nil || *last.SleepActualMin != 517 ||
		last.Recovery == nil || last.SleepPerformance == nil {
		t.Fatalf("the fixture must reach the scores, or the test proves nothing: %+v", last)
	}

	// The weekly estimate of another account learns 517 minutes.
	learner := uuid.New()
	if _, err := st.EnsureUser(ctx, store.Identity{ID: learner}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUser(ctx, learner, model.UserPatch{TZ: ptrTo("UTC")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantConsent(ctx, learner, "test", nil); err != nil {
		t.Fatal(err)
	}
	strain, recovery, asleep := 5.0, 80, 517
	for k := 0; k < 20; k++ {
		row := model.DailyScore{Day: time.Date(2026, 10, 2+k, 0, 0, 0, 0, time.UTC).Format("2006-01-02"),
			Strain: &strain, Recovery: &recovery, SleepActualMin: &asleep}
		if _, err := st.UpsertDailyScore(ctx, learner, store.ScoreRow{DailyScore: row}); err != nil {
			t.Fatal(err)
		}
	}
	lctx := logx.WithFields(context.Background(), &logx.Fields{UserID: learner.String()})
	if out, err := engine.LearnBaseline(lctx, learner, "2026-10-21"); err != nil || out != LearnApplied {
		t.Fatalf("learn: %s %v", out, err)
	}

	scored := map[string]bool{}
	msgs := map[string]int{}
	// Values no line may hold: the seeds, what the run computed from them
	// (recovery, sleep performance) and the learner's recovery of 80.
	forbid := logcheck.Forbidden{
		Texts:   []string{"61.7", "47.3", "14.7"},
		Numbers: []float64{517, 12345, 61.7, 47.3, 14.7, float64(recovery), float64(*last.Recovery), float64(*last.SleepPerformance)},
	}
	for _, n := range forbid.Numbers {
		if n == 1 || n == 10 || n == 11 {
			t.Fatalf("the scan would collide with a count of days or the day count: %v", n)
		}
	}
	lines, problems := logcheck.Check(buf.String(), logLineKeys, engineLogKeys, forbid)
	for _, p := range problems {
		t.Error(p)
	}
	for msg, recs := range lines {
		msgs[msg] = len(recs)
		for _, rec := range recs {
			if msg != "scored day" || rec["user_id"] != user.String() {
				continue
			}
			day, ok := rec["day"].(string)
			if !ok {
				t.Fatalf("a scored day names its day: %v", rec)
			}
			if rec["model_version"] != ModelVersion || rec["request_id"] != requestID {
				t.Errorf("a scored day names its model and its request: %v", rec)
			}
			scored[day] = true
		}
	}
	// Exact, not only a count in range: the run is known. Ten days follow the late night.
	if a := lines["rescored days after a history change"]; len(a) != 1 || a[0]["days"] != 10.0 || a[0]["through"] != "2026-10-10" {
		t.Errorf("the re-score after the late night is 10 days through 2026-10-10: %v", a)
	}
	// What operators follow a sync by stays: every day scored, the re-score, the estimate.
	for k := 0; k <= 10; k++ {
		if d := first.AddDate(0, 0, k).Format("2006-01-02"); !scored[d] {
			t.Errorf("no scored day line for %s", d)
		}
	}
	if msgs["rescored days after a history change"] == 0 || msgs["sleep baseline learned"] != 1 {
		t.Errorf("lines by message: %v", msgs)
	}
}
