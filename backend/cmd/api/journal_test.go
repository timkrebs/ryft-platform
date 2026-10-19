package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/behavior"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

func journalHandler(t *testing.T) (*handler, model.User, *store.Store) {
	t.Helper()
	pool := testPool(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Seed(context.Background(), pool, quiet); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)
	u, err := st.EnsureUser(context.Background(), store.Identity{ID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	return &handler{st: st, log: quiet, cache: newInsightCache()}, u, st
}

func call(t *testing.T, h http.HandlerFunc, u model.User, method, path, body string, pathValues ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k := 0; k+1 < len(pathValues); k += 2 {
		r.SetPathValue(pathValues[k], pathValues[k+1])
	}
	rec := httptest.NewRecorder()
	h(rec, asUser(r, u, ""))
	return rec
}

func TestJournalDayAPI(t *testing.T) {
	h, u, _ := journalHandler(t)
	berlin, _ := time.LoadLocation(u.TZ)
	yesterday := time.Now().In(berlin).AddDate(0, 0, -1).Format(dayLayout)
	alcohol, drinks := behavior.BuiltinID("alcohol").String(), behavior.BuiltinID("alcohol_drinks").String()

	rec := call(t, h.journalDay, u, "GET", "/v1/journal/"+yesterday, "", "day", yesterday)
	var d model.JournalDay
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if rec.Code != 200 || d.Answered || !d.Editable || len(d.Entries) != 0 {
		t.Fatalf("unanswered: %d %+v", rec.Code, d)
	}
	body := `{"entries": [{"behavior_id": "` + alcohol + `", "value_bool": true}, {"behavior_id": "` + drinks + `", "value_bool": true, "value_num": 3}]}`
	rec = call(t, h.putJournal, u, "PUT", "/v1/journal/"+yesterday, body, "day", yesterday)
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if rec.Code != 200 || !d.Answered || d.AnsweredAt == nil || len(d.Entries) != 2 {
		t.Fatalf("saved: %d %s", rec.Code, rec.Body)
	}
	old := time.Now().In(berlin).AddDate(0, 0, -15).Format(dayLayout)
	for name, c := range map[string]struct{ day, body string }{
		"15 days back":  {old, `{"entries": []}`},
		"unknown":       {yesterday, `{"entries": [{"behavior_id": "` + uuid.NewString() + `", "value_bool": true}]}`},
		"duplicate":     {yesterday, `{"entries": [{"behavior_id": "` + alcohol + `", "value_bool": true}, {"behavior_id": "` + alcohol + `", "value_bool": false}]}`},
		"count missing": {yesterday, `{"entries": [{"behavior_id": "` + drinks + `", "value_bool": true}]}`},
	} {
		if rec := call(t, h.putJournal, u, "PUT", "/v1/journal/"+c.day, c.body, "day", c.day); rec.Code != 422 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := call(t, h.putJournal, u, "PUT", "/v1/journal/x", `{}`, "day", "x"); rec.Code != 400 {
		t.Errorf("malformed day: %d", rec.Code)
	}
}

// Alcohol on every third of 60 days, and the mornings after them 15 points
// lower: the impact unlocks with a significant negative recovery effect, and
// yesterday's alcohol puts the card on the Insights screen.
func TestBehaviorImpactAPI(t *testing.T) {
	h, u, st := journalHandler(t)
	ctx := context.Background()
	berlin, _ := time.LoadLocation(u.TZ)
	now := time.Now().In(berlin)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, berlin)
	alcohol := behavior.BuiltinID("alcohol")
	kinds := map[uuid.UUID]string{alcohol: model.BehaviorYesNo}
	for k := 1; k <= 60; k++ {
		day := today.AddDate(0, 0, -k)
		yes := k%3 == 1 // yesterday included
		if err := st.PutJournalDay(ctx, u.ID, day.Format(dayLayout), []model.JournalEntry{{BehaviorID: alcohol, ValueBool: yes}}, kinds); err != nil {
			t.Fatal(err)
		}
		rec, hrv, perf := 72+k%4, 55.0, 90
		if yes {
			rec, hrv, perf = 57+k%4, 46.0, 85
		}
		morning := day.AddDate(0, 0, 1).Format(dayLayout)
		if _, err := st.UpsertDailyScore(ctx, u.ID, store.ScoreRow{DailyScore: model.DailyScore{Day: morning, Recovery: &rec, HRVms: &hrv,
			SleepPerformance: &perf, State: model.StateBalanced}}); err != nil {
			t.Fatal(err)
		}
	}
	rec := call(t, h.behaviorImpact, u, "GET", "/v1/behaviors/impact", "")
	var out model.BehaviorImpacts
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 || out.WindowDays != 90 {
		t.Fatalf("impact: %d %s", rec.Code, rec.Body)
	}
	first := out.Impacts[0]
	if first.Slug != "alcohol" || first.Status != model.ImpactUnlocked || first.NYes != 20 || first.NNo != 40 ||
		first.Recovery.Delta != -15 || !first.Recovery.Significant || first.Recovery.CIHi >= 0 || first.HRVPct.Delta != -16.4 {
		t.Fatalf("alcohol: %+v %+v %+v", first, first.Recovery, first.HRVPct)
	}
	if len(out.Impacts) != 8 || out.Impacts[1].Status != model.ImpactLocked || out.Impacts[1].Needed != (model.ImpactNeeded{Yes: 5, No: 5}) {
		t.Fatalf("the other seven enabled behaviors are locked: %d, %+v", len(out.Impacts), out.Impacts[1])
	}

	rec = call(t, h.insights, u, "GET", "/v1/insights", "")
	var cards []model.Insight
	_ = json.Unmarshal(rec.Body.Bytes(), &cards)
	found := false
	for _, c := range cards {
		found = found || (c.ID == "behavior-impact" && c.Title == "Alcohol yesterday" && strings.Contains(c.Body, "15 points lower"))
	}
	if !found {
		t.Fatalf("no behavior-impact card: %s", rec.Body)
	}
}
