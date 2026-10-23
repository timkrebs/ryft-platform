package scoring

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Keys a scoring log line must never carry: scores, inputs, the state and the
// learned sleep (docs/OPERATIONS.md → Data protection (logs)).
var healthLogKeys = []string{"readiness", "recovery", "strain", "state", "hrv", "rhr", "resp", "spo2",
	"sleep_min", "nap_min", "sleep_need", "sleep_performance", "muscular_trimp", "minutes", "steps", "kcal",
	"calibrating"}

// A scoring run at debug level — ten days in order, a late night that re-scores
// the days after it, a learned sleep baseline — logs which days it scored and
// nothing of the person's data: none of the keys above, and none of the seeded
// values (HRV 61.7 ms, resting HR 47.3, 14.7 breaths a minute, 517 minutes
// asleep, 12345 steps), whatever the key.
func TestEngineLogsNoHealthValues(t *testing.T) {
	st, _, user := engineStore(t)
	var buf bytes.Buffer
	engine := NewEngine(st, logx.NewWriter(&buf, "scoring", slog.LevelDebug))
	ctx := logx.WithFields(context.Background(), &logx.Fields{RequestID: "req-logs", UserID: user.String()})

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
	if d := scoresByDay(t, st, user, "2026-10-10", "2026-10-10")["2026-10-10"]; d.HRVms == nil || *d.HRVms != 61.7 ||
		d.RHR == nil || *d.RHR != 47.3 || d.SleepActualMin == nil || *d.SleepActualMin != 517 {
		t.Fatalf("the fixture must reach the scores, or the test proves nothing: %+v", d)
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
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("not a JSON line: %q", line)
		}
		delete(rec, "time") // 07:38:14.7… is a time, not a breath rate
		if b, _ := json.Marshal(rec); bytes.Contains(b, []byte("61.7")) || bytes.Contains(b, []byte("47.3")) ||
			bytes.Contains(b, []byte("14.7")) {
			t.Errorf("a seeded value in %s", line)
		}
		for _, k := range healthLogKeys {
			if _, ok := rec[k]; ok {
				t.Errorf("%q carries %s: %s", rec["msg"], k, line)
			}
		}
		for k, v := range rec {
			if f, ok := v.(float64); ok && (f == 517 || f == 12345 || f == 61.7 || f == 47.3 || f == 14.7) {
				t.Errorf("%q carries a seeded value as %s: %s", rec["msg"], k, line)
			}
		}
		msg, _ := rec["msg"].(string)
		msgs[msg]++
		if msg == "scored day" && rec["user_id"] == user.String() {
			if rec["model_version"] != ModelVersion || rec["request_id"] != "req-logs" {
				t.Errorf("a scored day names its model and its request: %s", line)
			}
			scored[rec["day"].(string)] = true
		}
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
