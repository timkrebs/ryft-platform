package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/behavior"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/periodization"
	"github.com/timkrebs/ryft/internal/scoring"
)

// Recovery without a watch through the api (D291–D296; docs/API.md → Recovery without a watch): the gates, the
// contract's numbers, validation, the N2R bedtime, the session RPE and the coaching briefing. All of it needs a
// Postgres (RYFT_TEST_POSTGRES_URL), as the other api tests do.

// recoveryRig is the nutrition rig with a clock the test moves (the api's coaching clock) and the built-in
// behaviors seeded (the journal's confounders read them).
type recoveryRig struct {
	*nutritionRig
	now time.Time
}

func newRecoveryRig(t *testing.T, configure ...func(*handler)) *recoveryRig {
	t.Helper()
	rr := &recoveryRig{now: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)}
	clock := func(h *handler) { h.coachingClock = func() time.Time { return rr.now } }
	rr.nutritionRig = newNutritionRig(t, append([]func(*handler){clock}, configure...)...)
	if err := db.Seed(context.Background(), rr.pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	return rr
}

// user is an account in UTC, with consent unless noConsent.
func (rr *recoveryRig) user(noConsent ...bool) uuid.UUID {
	rr.t.Helper()
	id := rr.account(noConsent...)
	if _, err := rr.pool.Exec(context.Background(), `UPDATE users SET tz='UTC' WHERE id=$1`, id); err != nil {
		rr.t.Fatal(err)
	}
	return id
}

// flags puts the accounts on the nutrition pilot and on recovery.checkin.
func (rr *recoveryRig) flags(pilot, recovery []uuid.UUID) {
	rr.t.Helper()
	ctx := context.Background()
	if _, _, err := rr.st.PutFlag(ctx, model.FeatureFlag{Key: model.FlagNutrition, Enabled: true, AllowUserIDs: pilot}); err != nil {
		rr.t.Fatal(err)
	}
	if _, _, err := rr.st.PutFlag(ctx, model.FeatureFlag{Key: model.FlagRecoveryCheckin, Enabled: true, AllowUserIDs: recovery}); err != nil {
		rr.t.Fatal(err)
	}
}

// call sends the request, expects the status and decodes the body into out.
func (rr *recoveryRig) call(user uuid.UUID, method, path string, body any, want int, out any) {
	rr.t.Helper()
	resp := rr.do(user, method, path, body)
	if resp.Code != want {
		rr.t.Fatalf("%s %s: %d, want %d: %s", method, path, resp.Code, want, resp.Body)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Body.Bytes(), out); err != nil {
			rr.t.Fatalf("%s %s: %v: %s", method, path, err, resp.Body)
		}
	}
}

// errorCode is the `code` of an error body, "" when it has none.
func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	code, _ := m["code"].(string)
	return code
}

func checkinBody(bed, wake string, tz, quality, freshness int, soreness bool) map[string]any {
	return map[string]any{"bedtime": bed, "wake_time": wake, "tz_offset_sec": tz, "perceived_sleep_quality": quality,
		"perceived_freshness": freshness, "soreness": soreness}
}

// night is an 8-hour night ending on the wake day at 06:00 UTC.
func night(wakeDay string, quality, freshness int) map[string]any {
	wake, _ := time.Parse(time.DateOnly, wakeDay)
	return checkinBody(wake.Add(-2*time.Hour).Format(time.RFC3339), wake.Add(6*time.Hour).Format(time.RFC3339), 0, quality, freshness, false)
}

const checkins = "/v1/recovery/checkins/"

// The two writes and the status answer 404 without a code while the flag is off; the reads of stored check-ins and
// both DELETEs work; consent gates both writes (warn acts as on) and neither read nor delete.
func TestRecoveryGates(t *testing.T) {
	rr := newRecoveryRig(t)
	a, noConsent := rr.user(), rr.user(true)
	body := checkinBody("2026-11-26T00:40:00Z", "2026-11-26T05:40:00Z", 0, 3, 3, false)
	workout := uuid.New()
	rpe := "/v1/workouts/" + workout.String() + "/rpe"

	// Flag off for everyone.
	for _, c := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodPut, checkins + "2026-11-26", body, 404},
		{http.MethodGet, "/v1/recovery/status", nil, 404},
		{http.MethodPut, rpe, map[string]any{"session_rpe": 8}, 404},
		{http.MethodGet, checkins + "2026-11-26", nil, 404}, // none stored
		{http.MethodGet, "/v1/recovery/checkins", nil, 200},
		{http.MethodDelete, checkins + "2026-11-26", nil, 204},
		{http.MethodDelete, rpe, nil, 404}, // not a workout of the account
	} {
		resp := rr.do(a, c.method, c.path, c.body)
		if resp.Code != c.want {
			t.Fatalf("flag off, %s %s: %d, want %d: %s", c.method, c.path, resp.Code, c.want, resp.Body)
		}
		if resp.Code == 404 && errorCode(t, resp.Body.Bytes()) != "" {
			t.Fatalf("the flag's 404 carries a code: %s", resp.Body)
		}
	}
	rr.call(uuid.Nil, http.MethodPut, checkins+"2026-11-26", body, 401, nil)

	// Flag on: consent decides the writes.
	rr.flags(nil, []uuid.UUID{a, noConsent})
	resp := rr.do(noConsent, http.MethodPut, checkins+"2026-11-26", body)
	if resp.Code != 403 || errorCode(t, resp.Body.Bytes()) != model.CodeConsentRequired {
		t.Fatalf("PUT without consent: %d %s", resp.Code, resp.Body)
	}
	resp = rr.do(noConsent, http.MethodPut, rpe, map[string]any{"session_rpe": 8})
	if resp.Code != 403 || errorCode(t, resp.Body.Bytes()) != model.CodeConsentRequired {
		t.Fatalf("rating without consent: %d %s", resp.Code, resp.Body)
	}
	rr.call(noConsent, http.MethodGet, "/v1/recovery/status", nil, 200, nil)
	rr.call(noConsent, http.MethodGet, "/v1/recovery/checkins", nil, 200, nil)
	rr.call(noConsent, http.MethodDelete, checkins+"2026-11-26", nil, 204, nil)
	rr.call(a, http.MethodPut, checkins+"2026-11-26", body, 200, nil)
	// The flag off again: what is stored stays readable and erasable, nothing is served.
	rr.flags(nil, nil)
	rr.call(a, http.MethodGet, checkins+"2026-11-26", nil, 200, nil)
	rr.call(a, http.MethodPut, checkins+"2026-11-26", body, 404, nil)
	rr.call(a, http.MethodDelete, checkins+"2026-11-26", nil, 204, nil)
}

// CONSENT_ENFORCE=warn serves other uploads with a header; these two writes refuse like Nutrition's (D295).
func TestRecoveryWritesRefuseWithoutConsentUnderWarn(t *testing.T) {
	rr := newRecoveryRig(t, func(h *handler) { h.beta.Consent = config.ModeWarn })
	noConsent := rr.user(true)
	rr.flags(nil, []uuid.UUID{noConsent})
	for _, path := range []string{checkins + "2026-11-26", "/v1/workouts/" + uuid.NewString() + "/rpe"} {
		resp := rr.do(noConsent, http.MethodPut, path, map[string]any{"session_rpe": 8})
		if resp.Code != 403 || errorCode(t, resp.Body.Bytes()) != model.CodeConsentRequired {
			t.Fatalf("PUT %s under warn: %d %s", path, resp.Code, resp.Body)
		}
	}
}

// The contract's numbers: the example (77), 5 h at 3/3 (29), 8 h at 9/9 (93); an identical PUT is the same
// check-in; nothing the user reported, no time and no score reaches a log line.
func TestRecoveryCheckinScoresAndIsIdempotent(t *testing.T) {
	rr := newRecoveryRig(t)
	a, b, c := rr.user(), rr.user(), rr.user()
	rr.flags(nil, []uuid.UUID{a, b, c})

	var got, again model.RecoveryCheckin
	example := checkinBody("2026-11-25T22:10:00Z", "2026-11-26T05:40:00Z", 3600, 7, 6, false)
	rr.call(a, http.MethodPut, checkins+"2026-11-26", example, 200, &got)
	want := model.RecoveryComponents{Wellness: 63, WellnessAbsolute: 63, Duration: 100}
	if got.Date != "2026-11-26" || got.SleepDurationMin != 450 || got.RecoveryScore != 77 || got.Components != want || got.AcuteDrop ||
		got.Confounders == nil || len(got.Confounders) != 0 || got.Confidence != model.RecoveryConfidenceLow || got.Superseded ||
		got.N2RBedtimeApplied || got.ModelVersion != scoring.ModelVersion || got.TZOffsetSec != 3600 || got.Soreness ||
		!got.Bedtime.Equal(time.Date(2026, 11, 25, 22, 10, 0, 0, time.UTC)) || got.CreatedAt.IsZero() {
		t.Fatalf("the contract's example: %+v", got)
	}
	rr.call(a, http.MethodPut, checkins+"2026-11-26", example, 200, &again)
	if !reflect.DeepEqual(got, again) {
		t.Fatalf("the same body twice: %+v then %+v", got, again)
	}
	var read model.RecoveryCheckin
	var list []model.RecoveryCheckin
	rr.call(a, http.MethodGet, checkins+"2026-11-26", nil, 200, &read)
	rr.call(a, http.MethodGet, "/v1/recovery/checkins?from=2026-11-25&to=2026-11-26", nil, 200, &list)
	if !reflect.DeepEqual(read, got) || len(list) != 1 || !reflect.DeepEqual(list[0], got) {
		t.Fatalf("the reads: %+v / %+v", read, list)
	}

	var short, long model.RecoveryCheckin
	rr.call(b, http.MethodPut, checkins+"2026-11-26", checkinBody("2026-11-26T00:40:00Z", "2026-11-26T05:40:00Z", 0, 3, 3, false), 200, &short)
	if short.SleepDurationMin != 300 || short.RecoveryScore != 29 || short.Components.Duration != 40 || short.Components.WellnessAbsolute != 22 || !short.AcuteDrop {
		t.Fatalf("5 h at 3/3: %+v", short)
	}
	rr.call(c, http.MethodPut, checkins+"2026-11-26", checkinBody("2026-11-25T22:00:00Z", "2026-11-26T06:00:00Z", 0, 9, 9, false), 200, &long)
	if long.SleepDurationMin != 480 || long.RecoveryScore != 93 || long.Components.Duration != 100 || long.Components.WellnessAbsolute != 89 || long.AcuteDrop {
		t.Fatalf("8 h at 9/9: %+v", long)
	}
	// Soreness is stored and shown, and moves nothing else.
	var sore model.RecoveryCheckin
	rr.call(c, http.MethodPut, checkins+"2026-11-26", checkinBody("2026-11-25T22:00:00Z", "2026-11-26T06:00:00Z", 0, 9, 9, true), 200, &sore)
	if !sore.Soreness || sore.RecoveryScore != long.RecoveryScore || sore.AcuteDrop || sore.Components != long.Components {
		t.Fatalf("soreness: %+v vs %+v", sore, long)
	}

	rr.call(a, http.MethodDelete, checkins+"2026-11-26", nil, 204, nil)
	rr.call(a, http.MethodDelete, checkins+"2026-11-26", nil, 204, nil)
	rr.call(a, http.MethodGet, checkins+"2026-11-26", nil, 404, nil)

	// The logs: the pattern of the route, none of what was reported.
	logs := rr.logs.String()
	for _, secret := range []string{"2026-11-25T22:10:00Z", "2026-11-26T05:40:00Z", "perceived", "recovery_score", "bedtime", "wake_time",
		"tz_offset_sec", "/v1/recovery/checkins/2026"} {
		if strings.Contains(logs, secret) {
			t.Errorf("a log line holds %q:\n%s", secret, logs)
		}
	}
	if !strings.Contains(logs, "/v1/recovery/checkins/{date}") {
		t.Errorf("the access line should name the route's pattern:\n%s", logs)
	}
}

func TestRecoveryCheckinValidation(t *testing.T) {
	rr := newRecoveryRig(t)
	a := rr.user()
	rr.flags(nil, []uuid.UUID{a})
	ok := checkinBody("2026-11-25T22:10:00Z", "2026-11-26T05:40:00Z", 3600, 7, 6, false)
	with := func(base map[string]any, k string, v any) map[string]any {
		m := map[string]any{}
		for key, val := range base {
			m[key] = val
		}
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		name, date string
		body       any
		want       int
	}{
		{"valid", "2026-11-26", ok, 200},
		{"a date that is not the wake day", "2026-11-25", ok, 422},
		{"a date that is not a date", "2026-13-45", ok, 400},
		{"15 days back", "2026-11-11", night("2026-11-11", 6, 6), 422},
		{"14 days back", "2026-11-12", night("2026-11-12", 6, 6), 200},
		{"tomorrow", "2026-11-27", night("2026-11-27", 6, 6), 422},
		{"6 minutes ahead of the clock", "2026-11-26", checkinBody("2026-11-26T02:00:00Z", "2026-11-26T10:06:00Z", 0, 6, 6, false), 422},
		{"4 minutes ahead of the clock", "2026-11-26", checkinBody("2026-11-26T02:00:00Z", "2026-11-26T10:04:00Z", 0, 6, 6, false), 200},
		{"59 minutes", "2026-11-26", checkinBody("2026-11-26T04:41:00Z", "2026-11-26T05:40:00Z", 0, 6, 6, false), 422},
		{"60 minutes", "2026-11-26", checkinBody("2026-11-26T04:40:00Z", "2026-11-26T05:40:00Z", 0, 6, 6, false), 200},
		{"960 minutes", "2026-11-26", checkinBody("2026-11-25T13:40:00Z", "2026-11-26T05:40:00Z", 0, 6, 6, false), 200},
		{"961 minutes", "2026-11-26", checkinBody("2026-11-25T13:39:00Z", "2026-11-26T05:40:00Z", 0, 6, 6, false), 422},
		{"wake as bedtime", "2026-11-26", checkinBody("2026-11-26T05:40:00Z", "2026-11-26T05:40:00Z", 0, 6, 6, false), 422},
		{"wake before bedtime", "2026-11-26", checkinBody("2026-11-26T06:40:00Z", "2026-11-26T05:40:00Z", 0, 6, 6, false), 422},
		{"an offset that is not a multiple of 60", "2026-11-26", with(ok, "tz_offset_sec", 30), 422},
		{"an offset beyond 14 hours", "2026-11-26", with(ok, "tz_offset_sec", 50460), 422},
		{"quality 0", "2026-11-26", with(ok, "perceived_sleep_quality", 0), 422},
		{"quality 11", "2026-11-26", with(ok, "perceived_sleep_quality", 11), 422},
		{"freshness 0", "2026-11-26", with(ok, "perceived_freshness", 0), 422},
		{"a fraction in an int field", "2026-11-26", `{"bedtime":"2026-11-25T22:10:00Z","wake_time":"2026-11-26T05:40:00Z","tz_offset_sec":3600,"perceived_sleep_quality":7.5,"perceived_freshness":6,"soreness":false}`, 400},
		{"an unknown field", "2026-11-26", with(ok, "mood", 3), 400},
		{"soreness missing", "2026-11-26", with(ok, "soreness", nil), 422},
		{"tz_offset_sec missing", "2026-11-26", with(ok, "tz_offset_sec", nil), 422},
		{"bedtime missing", "2026-11-26", with(ok, "bedtime", nil), 422},
		{"a string where a number goes", "2026-11-26", with(ok, "perceived_freshness", "6"), 400},
		{"not JSON", "2026-11-26", `{`, 400},
		{"two documents", "2026-11-26", `{"soreness":false} {"soreness":false}`, 400},
	}
	for _, c := range cases {
		resp := rr.do(a, http.MethodPut, checkins+c.date, c.body)
		if resp.Code != c.want {
			t.Errorf("%s: %d, want %d: %s", c.name, resp.Code, c.want, resp.Body)
		}
	}
	// Past 64 KiB the body is refused unread.
	big := with(ok, "padding", strings.Repeat("x", 70<<10))
	if resp := rr.do(a, http.MethodPut, checkins+"2026-11-26", big); resp.Code != 413 {
		t.Errorf("an oversized body: %d", resp.Code)
	}
	// The range read: malformed 400, inverted 422, over 400 days 422, 400 days fine.
	for path, want := range map[string]int{
		"/v1/recovery/checkins?from=2026-11-01&to=2026-11-30": 200,
		"/v1/recovery/checkins?from=nope":                     400,
		"/v1/recovery/checkins?from=2026-11-30&to=2026-11-01": 422,
		"/v1/recovery/checkins?from=2025-10-22&to=2026-11-26": 422, // 401 days
		"/v1/recovery/checkins?from=2025-10-23&to=2026-11-26": 200, // 400 days
		"/v1/recovery/checkins":                               200,
		checkins + "nope":                                     400,
	} {
		if resp := rr.do(a, http.MethodGet, path, nil); resp.Code != want {
			t.Errorf("GET %s: %d, want %d: %s", path, resp.Code, want, resp.Body)
		}
	}
	var list []model.RecoveryCheckin
	rr.call(a, http.MethodGet, "/v1/recovery/checkins?from=2026-11-01&to=2026-11-30", nil, 200, &list)
	if len(list) < 2 || list[0].Date > list[1].Date {
		t.Errorf("oldest first: %+v", list)
	}
}

// 14 nights at 6/6, then 5/5: z −1.85, a personal part of 4, wellness 32, an acute drop by z alone. Editing or
// deleting an earlier check-in rescores the later ones in the same call; 4 check-ins have no personal part.
func TestRecoveryCheckinPersonalBlendAndRescoring(t *testing.T) {
	rr := newRecoveryRig(t)
	a, b := rr.user(), rr.user()
	rr.flags(nil, []uuid.UUID{a, b})
	day := func(back int) string { return rr.now.AddDate(0, 0, -back).Format(time.DateOnly) }
	for back := 14; back >= 1; back-- {
		rr.call(a, http.MethodPut, checkins+day(back), night(day(back), 6, 6), 200, nil)
	}
	var today model.RecoveryCheckin
	rr.call(a, http.MethodPut, checkins+day(0), night(day(0), 5, 5), 200, &today)
	c := today.Components
	if c.WellnessAbsolute != 44 || c.WellnessPersonal == nil || *c.WellnessPersonal != 4 || c.Wellness != 32 || c.Duration != 100 ||
		c.Regularity == nil || *c.Regularity != 100 || !today.AcuteDrop || today.RecoveryScore != 62 || len(today.Confounders) != 0 {
		t.Fatalf("5/5 after 14 nights of 6/6: %+v", today)
	}
	// Editing the oldest night moves the personal part of today's, deleting the edit moves it back.
	rr.call(a, http.MethodPut, checkins+day(14), night(day(14), 10, 10), 200, nil)
	var moved model.RecoveryCheckin
	rr.call(a, http.MethodGet, checkins+day(0), nil, 200, &moved)
	if moved.Components.WellnessPersonal == nil || *moved.Components.WellnessPersonal == 4 {
		t.Fatalf("an edit of an earlier check-in did not rescore today's: %+v", moved.Components)
	}
	rr.call(a, http.MethodDelete, checkins+day(14), nil, 204, nil)
	var back model.RecoveryCheckin
	rr.call(a, http.MethodGet, checkins+day(0), nil, 200, &back)
	if back.Components.WellnessPersonal == nil || *back.Components.WellnessPersonal != 4 || back.Components.Wellness != 32 {
		t.Fatalf("after the delete: %+v", back.Components)
	}

	// Four check-ins before: no personal part, wellness is the absolute one, and no regularity from fewer than 3 nights.
	for d := 4; d >= 1; d-- {
		rr.call(b, http.MethodPut, checkins+day(d), night(day(d), 6, 6), 200, nil)
	}
	var first model.RecoveryCheckin
	rr.call(b, http.MethodPut, checkins+day(0), night(day(0), 5, 5), 200, &first)
	if first.Components.WellnessPersonal != nil || first.Components.Wellness != first.Components.WellnessAbsolute {
		t.Fatalf("four check-ins before: %+v", first.Components)
	}
	var second model.RecoveryCheckin
	rr.call(b, http.MethodGet, checkins+day(3), nil, 200, &second)
	if second.Components.Regularity != nil {
		t.Fatalf("two nights before are too few for regularity: %+v", second.Components)
	}
}

// The reported bedtime moves the day before's N2R at once (D293): its late-meal penalty, bedtime_source and the
// check-in's n2r_bedtime_applied; a measured sleep start beats it; the delete and the flag put it back.
func TestRecoveryCheckinBedtimeFeedsYesterdaysN2R(t *testing.T) {
	rr := newRecoveryRig(t)
	rr.now = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	a := rr.user()
	rr.flags([]uuid.UUID{a}, []uuid.UUID{a})
	nutrition := func(method, path string, body any, want int, out any) {
		t.Helper()
		rr.call(a, method, nutritionPrefix+path, body, want, out)
	}
	nutrition(http.MethodPut, "/n2r/profile", model.N2RProfile{BodyMassKG: 70, ExpectedBedtime: "23:00"}, 200, nil)
	nutrition(http.MethodPut, "/goals", model.NutritionGoalInput{Targets: map[string]float64{"energy_kcal": 3000}}, 200, nil)
	var food model.FoodDetail
	nutrition(http.MethodPost, "/foods/custom", map[string]any{
		"client_request_id": uuid.New(), "name": "Late dinner fixture",
		"quality_profile": map[string]any{
			"non_starchy_vegetable_fraction": .3, "whole_fruit_fraction": .2,
			"nuts_legumes_fraction": .03, "red_processed_meat_fraction": 0, "sugary_drink": false,
		},
		"nutrients": []map[string]any{
			{"code": "energy_kcal", "amount": 90, "unit": "kcal", "reference_amount": 100, "reference_unit": "g"},
			{"code": "fiber", "amount": 3, "unit": "g", "reference_amount": 100, "reference_unit": "g"},
		},
	}, 200, &food)
	nutrition(http.MethodPost, "/meals", model.NutritionMealInput{ClientRequestID: uuid.New(), ConsumedAt: time.Date(2026, 11, 24, 21, 30, 0, 0, time.UTC),
		Timezone: "UTC", MealType: "dinner", InputMethod: "manual_entry",
		Items: []model.NutritionMealItemInput{{FoodID: food.ID, Quantity: 1000, Unit: "g"}}}, 201, nil)
	yesterday := func() *model.N2RScore {
		t.Helper()
		var d model.NutritionDaily
		nutrition(http.MethodGet, "/daily?date=2026-11-24", nil, 200, &d)
		if d.N2R == nil {
			t.Fatalf("no N2R: %+v", d)
		}
		return d.N2R
	}
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }

	expected := yesterday()
	if expected.BedtimeSource != model.N2RBedtimeExpected || expected.Bedtime == nil || !expected.Bedtime.Equal(at("2026-11-24T23:00:00Z")) || expected.LateMealPenalty <= 0 {
		t.Fatalf("expected bedtime: %+v", expected)
	}
	var checkin model.RecoveryCheckin
	rr.call(a, http.MethodPut, checkins+"2026-11-25", checkinBody("2026-11-24T22:00:00Z", "2026-11-25T06:00:00Z", 0, 6, 6, false), 200, &checkin)
	if !checkin.N2RBedtimeApplied {
		t.Fatalf("the check-in's bedtime is applied: %+v", checkin)
	}
	reported := yesterday()
	if reported.BedtimeSource != model.N2RBedtimeCheckin || reported.Bedtime == nil || !reported.Bedtime.Equal(at("2026-11-24T22:00:00Z")) ||
		reported.LateMealPenalty <= expected.LateMealPenalty {
		t.Fatalf("a 21:30 meal 30 minutes before the reported bedtime must cost more than 90 minutes before the expected one: %+v vs %+v", reported, expected)
	}
	// The same N2R in the briefing's yesterday_n2r.
	if _, err := rr.pool.Exec(context.Background(), `UPDATE users SET birth_year=1990 WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	rr.call(a, http.MethodPut, "/v1/users/goal-profile", model.GoalProfile{Goal: periodization.CyclingRace, HeightCM: 178}, 200, nil)
	var briefing model.CoachingBriefing
	rr.call(a, http.MethodGet, "/v1/coaching/daily-briefing", nil, 200, &briefing)
	if briefing.Yesterday == nil || briefing.Yesterday.BedtimeSource != model.N2RBedtimeCheckin {
		t.Fatalf("yesterday_n2r: %+v", briefing.Yesterday)
	}
	// A measured sleep start beats the report; the check-in then says it is not applied.
	if _, err := rr.pool.Exec(context.Background(), `INSERT INTO daily_scores (user_id, day, sleep_start, sleep_actual_min) VALUES ($1, '2026-11-25', $2, 400)`,
		a, at("2026-11-24T23:30:00Z")); err != nil {
		t.Fatal(err)
	}
	measured := yesterday()
	if measured.BedtimeSource != model.N2RBedtimeWearable || !measured.Bedtime.Equal(at("2026-11-24T23:30:00Z")) {
		t.Fatalf("a measured sleep start: %+v", measured)
	}
	rr.call(a, http.MethodGet, checkins+"2026-11-25", nil, 200, &checkin)
	if checkin.N2RBedtimeApplied {
		t.Fatalf("a night with a measured sleep start does not use the check-in's: %+v", checkin)
	}
	if _, err := rr.pool.Exec(context.Background(), `DELETE FROM daily_scores WHERE user_id=$1`, a); err != nil {
		t.Fatal(err)
	}
	// The flag is a kill switch for the bedtime; the delete takes the check-in away for good.
	rr.flags([]uuid.UUID{a}, nil)
	if off := yesterday(); off.BedtimeSource != model.N2RBedtimeExpected || off.LateMealPenalty != expected.LateMealPenalty {
		t.Fatalf("flag off: %+v", off)
	}
	rr.flags([]uuid.UUID{a}, []uuid.UUID{a})
	if on := yesterday(); on.BedtimeSource != model.N2RBedtimeCheckin {
		t.Fatalf("flag on again: %+v", on)
	}
	rr.call(a, http.MethodDelete, checkins+"2026-11-25", nil, 204, nil)
	if back := yesterday(); back.BedtimeSource != model.N2RBedtimeExpected || back.LateMealPenalty != expected.LateMealPenalty || !back.Bedtime.Equal(*expected.Bedtime) {
		t.Fatalf("after the delete: %+v", back)
	}
}

// The status: 04:00 local asks, 03:59 does not; a day watch's recovery is not a wearable night; a wearable night
// supersedes the check-in and deleting it brings the check-in back; fields the contract names are always present.
func TestRecoveryStatusAsksFromFourAndSupersedes(t *testing.T) {
	rr := newRecoveryRig(t)
	a := rr.user()
	rr.flags(nil, []uuid.UUID{a})
	status := func(clock string) (model.RecoveryStatus, map[string]any) {
		t.Helper()
		rr.now, _ = time.Parse(time.RFC3339, "2026-11-26T"+clock+":00Z")
		resp := rr.do(a, http.MethodGet, "/v1/recovery/status", nil)
		if resp.Code != 200 {
			t.Fatalf("status at %s: %d %s", clock, resp.Code, resp.Body)
		}
		var s model.RecoveryStatus
		var raw map[string]any
		if err := json.Unmarshal(resp.Body.Bytes(), &s); err != nil || json.Unmarshal(resp.Body.Bytes(), &raw) != nil {
			t.Fatal(err)
		}
		return s, raw
	}
	s, raw := status("03:59")
	if s.NeedsCheckin || s.Source != model.RecoverySourceNone || s.Date != "2026-11-26" {
		t.Fatalf("03:59: %+v", s)
	}
	for _, key := range []string{"confidence", "recovery_score", "checkin", "suggested_bedtime", "suggested_wake_time", "suggestion_basis"} {
		if v, present := raw[key]; !present || v != nil {
			t.Fatalf("%s must be present and null without a basis: %v", key, raw)
		}
	}
	if s, _ = status("04:00"); !s.NeedsCheckin {
		t.Fatalf("04:00 with nothing: %+v", s)
	}
	// A day watch: resting heart rate gives the day a recovery, but no night.
	if _, err := rr.pool.Exec(context.Background(), `INSERT INTO daily_scores (user_id, day, recovery, rhr) VALUES ($1, '2026-11-26', 64, 52)`, a); err != nil {
		t.Fatal(err)
	}
	if s, _ = status("10:00"); s.Source != model.RecoverySourceNone || !s.NeedsCheckin {
		t.Fatalf("a day watch is asked: %+v", s)
	}
	var checkin model.RecoveryCheckin
	rr.call(a, http.MethodPut, checkins+"2026-11-26", night("2026-11-26", 7, 7), 200, &checkin)
	if checkin.Superseded {
		t.Fatalf("a day watch's recovery does not supersede: %+v", checkin)
	}
	if s, _ = status("10:00"); s.Source != model.RecoverySourceCheckin || s.NeedsCheckin || s.Checkin == nil || *s.RecoveryScore != checkin.RecoveryScore {
		t.Fatalf("with a check-in: %+v", s)
	}
	// Asleep stages from a watch, with the recovery: a wearable night.
	if _, err := rr.pool.Exec(context.Background(), `UPDATE daily_scores SET sleep_has_stages=true, hrv_ms=48 WHERE user_id=$1`, a); err != nil {
		t.Fatal(err)
	}
	s, _ = status("10:00")
	if s.Source != model.RecoverySourceWearable || *s.Confidence != model.RecoveryConfidenceHigh || *s.RecoveryScore != 64 || s.NeedsCheckin || s.Checkin == nil || !s.Checkin.Superseded {
		t.Fatalf("a wearable night: %+v", s)
	}
	rr.call(a, http.MethodGet, checkins+"2026-11-26", nil, 200, &checkin)
	if !checkin.Superseded {
		t.Fatalf("the check-in stays readable, superseded: %+v", checkin)
	}
	if _, err := rr.pool.Exec(context.Background(), `DELETE FROM daily_scores WHERE user_id=$1`, a); err != nil {
		t.Fatal(err)
	}
	if s, _ = status("10:00"); s.Source != model.RecoverySourceCheckin {
		t.Fatalf("the check-in is current again: %+v", s)
	}
}

// The session RPE: the routes, the Workout fields, ownership, steps of 0.5, the hidden duplicate; the rating
// feeds neither day strain nor the workout's trimp, and queues nothing for scoring.
func TestSessionRPERoutes(t *testing.T) {
	rr := newRecoveryRig(t)
	ctx := context.Background()
	a, b := rr.user(), rr.user()
	rr.flags(nil, []uuid.UUID{a, b})
	start := time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC)
	workout := model.Workout{ID: uuid.New(), Activity: "strength", Start: start, End: start.Add(time.Hour), Source: "Tim's iPhone", WasUserEntered: true}
	if _, err := rr.st.UpsertWorkouts(ctx, a, []model.Workout{workout}); err != nil {
		t.Fatal(err)
	}
	theirs := model.Workout{ID: uuid.New(), Activity: "strength", Start: start, End: start.Add(time.Hour), Source: "Watch"}
	if _, err := rr.st.UpsertWorkouts(ctx, b, []model.Workout{theirs}); err != nil {
		t.Fatal(err)
	}
	path := "/v1/workouts/" + workout.ID.String()
	var before model.WorkoutDetail
	rr.call(a, http.MethodGet, path, nil, 200, &before)
	if before.SessionRPE != nil || before.SessionRPELoad != nil {
		t.Fatalf("unrated: %+v", before.Workout)
	}

	var rated model.Workout
	rr.call(a, http.MethodPut, path+"/rpe", map[string]any{"session_rpe": 8}, 200, &rated)
	if rated.ID != workout.ID || rated.SessionRPE == nil || *rated.SessionRPE != 8 || rated.SessionRPELoad == nil || *rated.SessionRPELoad != 480 {
		t.Fatalf("the rated workout: %+v", rated)
	}
	for _, c := range []struct {
		name string
		body any
		want int
	}{
		{"half steps", map[string]any{"session_rpe": 6.5}, 200},
		{"7.25", map[string]any{"session_rpe": 7.25}, 422},
		{"0.5", map[string]any{"session_rpe": 0.5}, 422},
		{"10.5", map[string]any{"session_rpe": 10.5}, 422},
		{"10", map[string]any{"session_rpe": 10}, 200},
		{"missing", map[string]any{}, 422},
		{"a string", map[string]any{"session_rpe": "8"}, 400},
		{"an unknown field", map[string]any{"session_rpe": 8, "effort": 3}, 400},
	} {
		if resp := rr.do(a, http.MethodPut, path+"/rpe", c.body); resp.Code != c.want {
			t.Errorf("%s: %d, want %d: %s", c.name, resp.Code, c.want, resp.Body)
		}
	}
	rr.call(a, http.MethodPut, path+"/rpe", map[string]any{"session_rpe": 8}, 200, nil)

	// Every route that returns workouts carries the rating; day strain and the workout's trimp are as before.
	var after model.WorkoutDetail
	rr.call(a, http.MethodGet, path, nil, 200, &after)
	if after.SessionRPE == nil || *after.SessionRPE != 8 || *after.SessionRPELoad != 480 || !reflect.DeepEqual(after.TRIMP, before.TRIMP) ||
		!reflect.DeepEqual(after.Strain, before.Strain) {
		t.Fatalf("the workout: %+v", after)
	}
	var listed []model.Workout
	rr.call(a, http.MethodGet, "/v1/workouts?from=2026-11-25&to=2026-11-25", nil, 200, &listed)
	var day model.DayDetail
	rr.call(a, http.MethodGet, "/v1/days/2026-11-25", nil, 200, &day)
	if len(listed) != 1 || listed[0].SessionRPE == nil || len(day.Workouts) != 1 || day.Workouts[0].SessionRPE == nil || *day.Workouts[0].SessionRPELoad != 480 {
		t.Fatalf("lists: %+v %+v", listed, day.Workouts)
	}
	if n := countRowsAPI(t, rr, `SELECT (SELECT count(*) FROM score_queue) + (SELECT count(*) FROM outbox)`); n != 0 {
		t.Fatalf("a rating must not queue scoring: %d rows", n)
	}

	// Another account's workout is not found, for the write and the delete; an unknown id too.
	rr.call(a, http.MethodPut, "/v1/workouts/"+theirs.ID.String()+"/rpe", map[string]any{"session_rpe": 5}, 404, nil)
	rr.call(a, http.MethodDelete, "/v1/workouts/"+theirs.ID.String()+"/rpe", nil, 404, nil)
	rr.call(a, http.MethodDelete, "/v1/workouts/"+uuid.NewString()+"/rpe", nil, 404, nil)
	rr.call(a, http.MethodPut, "/v1/workouts/not-a-uuid/rpe", map[string]any{"session_rpe": 5}, 400, nil)
	if n := countRowsAPI(t, rr, `SELECT count(*) FROM workout_ratings WHERE user_id=$1`, b); n != 0 {
		t.Fatalf("another account's workout was rated: %d", n)
	}

	// A hidden duplicate is rated through its primary.
	dup := model.Workout{ID: uuid.New(), Activity: "strength", Start: start.Add(time.Minute), End: start.Add(time.Hour), Source: "Garmin"}
	if _, err := rr.st.UpsertWorkouts(ctx, a, []model.Workout{dup}); err != nil {
		t.Fatal(err)
	}
	if _, err := rr.pool.Exec(ctx, `UPDATE workouts SET duplicate_of=$2 WHERE user_id=$1 AND id=$3`, a, workout.ID, dup.ID); err != nil {
		t.Fatal(err)
	}
	rr.call(a, http.MethodPut, "/v1/workouts/"+dup.ID.String()+"/rpe", map[string]any{"session_rpe": 5}, 422, nil)

	// A re-sync of the same HealthKit workout keeps the rating; DELETE removes it, twice is fine, and it works
	// with the flag off (so does the rating's display).
	workout.End = start.Add(90 * time.Minute)
	if _, err := rr.st.UpsertWorkouts(ctx, a, []model.Workout{workout}); err != nil {
		t.Fatal(err)
	}
	rr.call(a, http.MethodGet, path, nil, 200, &after)
	if after.SessionRPE == nil || *after.SessionRPELoad != 720 {
		t.Fatalf("a re-sync lost the rating (or kept the load of the old length): %+v", after.Workout)
	}
	rr.flags(nil, nil)
	rr.call(a, http.MethodGet, path, nil, 200, &after)
	if after.SessionRPE == nil {
		t.Fatal("stored ratings stay readable while the flag is off")
	}
	rr.call(a, http.MethodDelete, path+"/rpe", nil, 204, nil)
	rr.call(a, http.MethodDelete, path+"/rpe", nil, 204, nil)
	rr.call(a, http.MethodGet, path, nil, 200, &after)
	if after.SessionRPE != nil || after.SessionRPELoad != nil {
		t.Fatalf("deleted: %+v", after.Workout)
	}
}

func countRowsAPI(t *testing.T, rr *recoveryRig, q string, args ...any) int {
	t.Helper()
	var n int
	if err := rr.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// pilot sets an account up for the coaching briefing: birth year, an N2R profile and a goal.
func (rr *recoveryRig) pilot(user uuid.UUID, sex, goal string) {
	rr.t.Helper()
	if _, err := rr.pool.Exec(context.Background(), `UPDATE users SET birth_year=1990 WHERE id=$1`, user); err != nil {
		rr.t.Fatal(err)
	}
	rr.call(user, http.MethodPut, nutritionPrefix+"/n2r/profile", model.N2RProfile{Sex: sex, BodyMassKG: 70, ExpectedBedtime: "23:15"}, 200, nil)
	rr.call(user, http.MethodPut, "/v1/users/goal-profile", model.GoalProfile{Goal: goal, HeightCM: 178}, 200, nil)
}

func (rr *recoveryRig) briefing(user uuid.UUID) model.CoachingBriefing {
	rr.t.Helper()
	var out model.CoachingBriefing
	rr.call(user, http.MethodGet, "/v1/coaching/daily-briefing", nil, 200, &out)
	return out
}

// 60 minutes at RPE 8 with no heart rate is 151 (sex unspecified), 143 male, 157 female of coaching load; heart rate
// wins and the rating only shows; the flag off or the rating deleted takes it away.
func TestRatedWorkoutCountsInTheCoachingLoad(t *testing.T) {
	rr := newRecoveryRig(t)
	ctx := context.Background()
	a := rr.user()
	rr.flags([]uuid.UUID{a}, []uuid.UUID{a})
	rr.pilot(a, "unspecified", periodization.CyclingRace)
	start := time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC)
	hk := model.Workout{ID: uuid.New(), Activity: "strength", Start: start, End: start.Add(time.Hour), Source: "Tim's iPhone", WasUserEntered: true}
	if _, err := rr.st.UpsertWorkouts(ctx, a, []model.Workout{hk}); err != nil {
		t.Fatal(err)
	}
	acute := func() float64 { return rr.briefing(a).Load.Acute }
	if got := acute(); got != 0 {
		t.Fatalf("an unrated workout without heart rate has no load: %v", got)
	}
	rr.call(a, http.MethodPut, "/v1/workouts/"+hk.ID.String()+"/rpe", map[string]any{"session_rpe": 8}, 200, nil)
	for sex, want := range map[string]float64{"unspecified": 151, "male": 143, "female": 157} {
		rr.call(a, http.MethodPut, nutritionPrefix+"/n2r/profile", model.N2RProfile{Sex: sex, BodyMassKG: 70, ExpectedBedtime: "23:15"}, 200, nil)
		if got := acute(); math.Round(got) != want {
			t.Errorf("sex %s: acute load %v, want %v", sex, got, want)
		}
	}
	rr.call(a, http.MethodPut, nutritionPrefix+"/n2r/profile", model.N2RProfile{Sex: "unspecified", BodyMassKG: 70, ExpectedBedtime: "23:15"}, 200, nil)
	rr.call(a, http.MethodPut, "/v1/workouts/"+hk.ID.String()+"/rpe", map[string]any{"session_rpe": 3}, 200, nil)
	if got := acute(); math.Round(got) != 23 {
		t.Errorf("60 minutes at 3: %v, want 23", got)
	}
	// The kill switch: stored, readable, counted nowhere.
	rr.flags([]uuid.UUID{a}, nil)
	if got := acute(); got != 0 {
		t.Errorf("flag off: %v, want no load", got)
	}
	rr.flags([]uuid.UUID{a}, []uuid.UUID{a})
	if got := acute(); math.Round(got) != 23 {
		t.Errorf("flag on again: %v", got)
	}
	rr.call(a, http.MethodDelete, "/v1/workouts/"+hk.ID.String()+"/rpe", nil, 204, nil)
	if got := acute(); got != 0 {
		t.Errorf("deleted rating: %v", got)
	}
	// With heart rate (and a resting heart rate to take a reserve from) the measurement wins and the rating only shows.
	hrMax, hrRest := 190.0, 50.0
	rr.call(a, http.MethodPut, "/v1/users/goal-profile", model.GoalProfile{Goal: periodization.CyclingRace, HeightCM: 178, HRMax: &hrMax, HRRest: &hrRest}, 200, nil)
	withHR := model.Workout{ID: uuid.New(), Activity: "running", Start: start.Add(-24 * time.Hour), End: start.Add(-23 * time.Hour), Source: "Watch"}
	avg, max := 150.0, 175.0
	withHR.AvgHR, withHR.MaxHR = &avg, &max
	if _, err := rr.st.UpsertWorkouts(ctx, a, []model.Workout{withHR}); err != nil {
		t.Fatal(err)
	}
	measured := acute()
	rr.call(a, http.MethodPut, "/v1/workouts/"+withHR.ID.String()+"/rpe", map[string]any{"session_rpe": 10}, 200, nil)
	if got := acute(); got != measured {
		t.Errorf("a rated workout with heart rate: %v, was %v: the rating must only show", got, measured)
	}
}

// Readiness from a check-in where the HRV path has nothing (D294): the table of the contract on a fat-loss account;
// the journal of the day before explains a drop and the wake day does not; soreness only adds its reason; the neutral
// readiness_display and recovery are in the briefing; the flag off gives the briefing of an account without a check-in.
func TestBriefingReadinessFromACheckin(t *testing.T) {
	rr := newRecoveryRig(t)
	ctx := context.Background()
	a, b := rr.user(), rr.user()
	rr.flags([]uuid.UUID{a, b}, []uuid.UUID{a})
	rr.pilot(a, "unspecified", periodization.FatLoss)
	rr.pilot(b, "unspecified", periodization.FatLoss)
	// Both accounts have the same workout; a also rates it.
	start := time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC)
	for _, u := range []uuid.UUID{a, b} {
		w := model.Workout{ID: uuid.New(), Activity: "strength", Start: start, End: start.Add(time.Hour), Source: "Tim's iPhone", WasUserEntered: true}
		if _, err := rr.st.UpsertWorkouts(ctx, u, []model.Workout{w}); err != nil {
			t.Fatal(err)
		}
		if u == a {
			rr.call(a, http.MethodPut, "/v1/workouts/"+w.ID.String()+"/rpe", map[string]any{"session_rpe": 8}, 200, nil)
		}
	}
	journal := func(day, slug string) {
		t.Helper()
		if _, err := rr.pool.Exec(ctx, `INSERT INTO journal_days (user_id, day) VALUES ($1, $2) ON CONFLICT DO NOTHING`, a, day); err != nil {
			t.Fatal(err)
		}
		if _, err := rr.pool.Exec(ctx, `INSERT INTO journal_entries (user_id, day, behavior_id, value_bool) VALUES ($1, $2, $3, true)`, a, day, behavior.BuiltinID(slug)); err != nil {
			t.Fatal(err)
		}
	}
	put := func(quality, freshness int, soreness bool) {
		t.Helper()
		rr.call(a, http.MethodPut, checkins+"2026-11-26",
			checkinBody("2026-11-26T00:40:00Z", "2026-11-26T05:40:00Z", 3600, quality, freshness, soreness), 200, nil)
	}
	carbFloor := 4.5 * 70

	// No check-in yet: the HRV path has nothing, readiness is UNKNOWN, and the display name says the same.
	empty := rr.briefing(a)
	if empty.Readiness != periodization.ReadinessUnknown || empty.ReadinessDisplay != periodization.ReadinessUnknown || empty.Recovery == nil ||
		empty.Recovery.Source != model.RecoverySourceNone || !empty.Recovery.NeedsCheckin {
		t.Fatalf("no check-in: %+v %+v", empty, empty.Recovery)
	}
	// A good morning: OPTIMAL, no reasons.
	put(7, 6, false)
	got := rr.briefing(a)
	if got.Readiness != periodization.ReadinessOptimal || len(got.Reasons) != 0 || got.ReadinessDisplay != got.Readiness || got.Recovery == nil ||
		got.Recovery.Source != model.RecoverySourceCheckin || got.Recovery.Checkin == nil || got.Plan == nil || got.Plan.CarbsG >= carbFloor {
		t.Fatalf("a good morning: %+v", got)
	}
	// A drop with nothing to explain it: the new state, the protective plan, the reported reason.
	put(3, 4, false)
	got = rr.briefing(a)
	if got.Readiness != periodization.ReadinessLowUnexplained || got.ReadinessDisplay != periodization.ReadinessLowUnexplained ||
		!reflect.DeepEqual(got.Reasons, []string{"checkin_low_quality_and_freshness"}) || got.Plan == nil || !got.Plan.DeficitSuspended || got.Plan.CarbsG < carbFloor {
		t.Fatalf("an unexplained drop: %+v plan %+v", got, got.Plan)
	}
	if got.Recovery.RecoveryScore == nil || *got.Recovery.RecoveryScore != 31 || got.Recovery.Source != model.RecoverySourceCheckin ||
		*got.Recovery.Confidence != model.RecoveryConfidenceLow || got.Recovery.NeedsCheckin {
		t.Fatalf("the status in the briefing: %+v", got.Recovery)
	}
	// Soreness changes only the reasons.
	put(3, 4, true)
	sore := rr.briefing(a)
	if sore.Readiness != got.Readiness || !reflect.DeepEqual(sore.Reasons, []string{"checkin_low_quality_and_freshness", "soreness_reported"}) ||
		sore.Plan.CarbsG != got.Plan.CarbsG || sore.Recovery.Checkin.RecoveryScore != got.Recovery.Checkin.RecoveryScore {
		t.Fatalf("soreness: %+v", sore)
	}
	// The journal of the wake day is not the evening before: still unexplained. The day before explains it.
	journal("2026-11-26", "sick")
	put(3, 4, false)
	if b := rr.briefing(a); b.Readiness != periodization.ReadinessLowUnexplained {
		t.Fatalf("sick on the wake day only: %+v", b.Readiness)
	}
	journal("2026-11-25", "late_meal")
	put(3, 4, false)
	got = rr.briefing(a)
	if got.Readiness != periodization.ReadinessLifestyle || !reflect.DeepEqual(got.Reasons, []string{"journal_late_meal", "checkin_low_quality_and_freshness"}) ||
		got.Plan.CarbsG >= carbFloor {
		t.Fatalf("a late meal the evening before: %+v", got)
	}
	// Feeling sick the evening before explains it too, and the plan stays protective.
	journal("2026-11-25", "sick")
	put(3, 4, false)
	got = rr.briefing(a)
	if got.Readiness != periodization.ReadinessLifestyle || !slicesContain(got.Reasons, "journal_sick") || !got.Plan.DeficitSuspended || got.Plan.CarbsG < carbFloor {
		t.Fatalf("sick the evening before: %+v plan %+v", got, got.Plan)
	}

	// The kill switch: the briefing is that of the account without a check-in or a rating (b), but for the
	// recovery it does not serve; readiness_display is there either way.
	rr.flags([]uuid.UUID{a, b}, nil)
	off, plain := rr.briefing(a), rr.briefing(b)
	if off.Recovery != nil || plain.Recovery != nil || off.Readiness != periodization.ReadinessUnknown || off.ReadinessDisplay != periodization.ReadinessUnknown {
		t.Fatalf("flag off: %+v", off)
	}
	if !reflect.DeepEqual(off, plain) {
		t.Fatalf("with the flag off a check-in and a rating change nothing:\n%+v\n%+v", off, plain)
	}
	rr.flags([]uuid.UUID{a, b}, []uuid.UUID{a})
	if on := rr.briefing(a); on.Recovery == nil || on.Readiness != periodization.ReadinessLifestyle || on.Load.Acute == off.Load.Acute {
		t.Fatalf("flag on again: %+v", on)
	}
}

func slicesContain(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
