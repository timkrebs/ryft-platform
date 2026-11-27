package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

func TestDaysFitnessTiles(t *testing.T) {
	pool := testPool(t)
	st := store.New(pool)
	ctx := context.Background()
	const first, day, unscored = "2026-11-18", "2026-11-27", "2026-11-28"
	const zone = "Europe/Berlin"
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	account := func() model.User {
		t.Helper()
		u, err := st.EnsureUser(ctx, store.Identity{ID: uuid.New()})
		if err != nil {
			t.Fatal(err)
		}
		u, err = st.UpdateUser(ctx, u.ID, model.UserPatch{TZ: ptr(zone)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	own, other := account(), account()
	for _, date := range []string{first, day} {
		row := model.DailyScore{Day: date, State: model.StateCalibrating, ActiveMin: ptr(24)}
		if _, err := st.UpsertDailyScore(ctx, own.ID, store.ScoreRow{DailyScore: row}); err != nil {
			t.Fatal(err)
		}
	}
	sample := func(typ, unit, wall string, value float64) model.Sample {
		t.Helper()
		at, err := time.ParseInLocation("2006-01-02 15:04", wall, loc)
		if err != nil {
			t.Fatal(err)
		}
		return model.Sample{Type: typ, Start: at, End: at, Value: value, Unit: unit, Source: "Watch"}
	}
	vo2 := func(wall string, value float64) model.Sample {
		return sample(model.SampleVO2Max, "ml/kg/min", wall, value)
	}
	hrr := func(wall string, value float64) model.Sample {
		return sample(model.SampleHeartRateRecovery, "bpm", wall, value)
	}
	newest := vo2(day+" 23:50", 48.04)
	if _, err := st.UpsertSamples(ctx, own.ID, []model.Sample{
		vo2(first+" 16:00", 45.46), hrr(day+" 09:00", 31.3), hrr(day+" 23:50", 33.95),
		vo2(unscored+" 00:00", 62), hrr(unscored+" 00:00", 82),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSamples(ctx, other.ID, []model.Sample{vo2(day+" 23:55", 60), hrr(day+" 23:55", 90)}); err != nil {
		t.Fatal(err)
	}

	h := &handler{st: st}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/days", h.days)
	mux.HandleFunc("GET /v1/days/{day}", h.day)
	get := func(path string, out any) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodGet, path, nil), own))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	var detail model.DayDetail
	get("/v1/days/"+day, &detail)
	if detail.Score.VO2Max == nil || *detail.Score.VO2Max != 45.5 || detail.Score.VO2MaxDelta7D != nil {
		t.Fatalf("before the late reading: want VO2 max 45.5 and no delta, got %+v", detail.Score)
	}
	if _, err := st.UpsertSamples(ctx, own.ID, []model.Sample{newest}); err != nil {
		t.Fatal(err)
	}
	checkScore := func(score model.DailyScore) {
		t.Helper()
		if score.Day != day || score.VO2Max == nil || *score.VO2Max != 48 || score.VO2MaxDelta7D == nil || *score.VO2MaxDelta7D != 2.6 ||
			score.HRRBpm == nil || *score.HRRBpm != 32.6 || score.ActiveMin == nil || *score.ActiveMin != 24 {
			t.Errorf("want the caller's fitness tiles on %s, got %+v", day, score)
		}
	}
	var rows []model.DailyScore
	get("/v1/days?from="+first+"&to="+unscored, &rows)
	if len(rows) != 2 || rows[0].Day != first || rows[0].VO2Max == nil || *rows[0].VO2Max != 45.5 || rows[0].HRRBpm != nil {
		t.Fatalf("want two stored rows with only the older reading on the first day, got %+v", rows)
	}
	checkScore(rows[1])
	get("/v1/days/"+day, &detail)
	checkScore(detail.Score)
	get("/v1/days/"+unscored, &detail)
	if detail.Score.Day != unscored || detail.Score.VO2Max != nil || detail.Score.VO2MaxDelta7D != nil || detail.Score.HRRBpm != nil || detail.Score.ActiveMin != nil {
		t.Errorf("want null fitness tiles on the unscored placeholder, got %+v", detail.Score)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM samples WHERE user_id=$1 AND start_at=$2`, own.ID, newest.Start); err != nil {
		t.Fatal(err)
	}
	get("/v1/days/"+day, &detail)
	if detail.Score.VO2Max == nil || *detail.Score.VO2Max != 45.5 || detail.Score.VO2MaxDelta7D != nil ||
		detail.Score.HRRBpm == nil || *detail.Score.HRRBpm != 31.3 {
		t.Errorf("want the remaining readings after deletion, got %+v", detail.Score)
	}
}
