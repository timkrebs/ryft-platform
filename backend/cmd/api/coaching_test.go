package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"

	"github.com/timkrebs/ryft/internal/energy"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/periodization"
	"github.com/timkrebs/ryft/internal/wearables"
)

func TestCoachingWebhookToBriefingAndEvaluation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	const secret = "fixture-only-garmin-signing-key-at-least-32"
	var h *handler
	rig := newNutritionRig(t, func(handler *handler) {
		h = handler
		h.partners.secrets = map[string]string{"garmin": secret, "suunto": secret}
		h.coachingClock = func() time.Time { return now }
	})
	a, b := rig.account(), rig.account(true)
	if _, _, err := rig.st.PutFlag(ctx, model.FeatureFlag{Key: model.FlagNutrition, Enabled: true, AllowUserIDs: []uuid.UUID{a, b}}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.pool.Exec(ctx, `UPDATE users SET birth_year=1990,tz='UTC' WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	check := func(user uuid.UUID, method, path string, body any, want int, out any) {
		t.Helper()
		r := rig.do(user, method, path, body)
		if r.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, r.Code, r.Body)
		}
		if out != nil {
			if err := json.Unmarshal(r.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	gp := model.GoalProfile{Goal: periodization.CyclingRace, HeightCM: 178}
	check(b, http.MethodPut, "/v1/users/goal-profile", gp, 403, nil)
	check(a, http.MethodPut, "/v1/nutrition/n2r/profile", model.N2RProfile{Sex: "male", BodyMassKG: 70, ExpectedBedtime: "23:15"}, 200, nil)
	check(a, http.MethodPut, "/v1/users/goal-profile", gp, 200, nil)
	if _, err := rig.pool.Exec(ctx, `INSERT INTO nutrition.wearable_links(provider,provider_user,user_id) VALUES('garmin','fixture-athlete',$1)`, a); err != nil {
		t.Fatal(err)
	}
	start := now.Add(-100 * time.Minute)
	session := mesgdef.Session{StartTime: start, Timestamp: start.Add(80 * time.Minute), Sport: typedef.SportCycling,
		TotalTimerTime: 4800000, TotalElapsedTime: 4800000, TotalWork: 725000, NormalizedPower: 225, ThresholdPower: 250}
	var fit bytes.Buffer
	if err := encoder.New(&fit).Encode(&proto.FIT{Messages: []proto.Message{session.ToMesg(nil)}}); err != nil {
		t.Fatal(err)
	}
	fetch := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-fetch-token" || r.URL.Query().Get("provider_user") != "fixture-suunto" ||
			r.URL.Query().Get("workout_id") != "fit-777" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(fit.Bytes())
	}))
	defer fetch.Close()
	target, err := url.Parse(fetch.URL)
	if err != nil {
		t.Fatal(err)
	}
	h.partners.fetch = wearables.Fetchers{"suunto": target}
	h.partners.transport = fetch.Client().Transport
	h.partners.token = "fixture-fetch-token"
	if _, err := rig.pool.Exec(ctx, `INSERT INTO nutrition.wearable_links(provider,provider_user,user_id) VALUES('suunto','fixture-suunto',$1)`, a); err != nil {
		t.Fatal(err)
	}
	mirror := model.Workout{ID: uuid.New(), Activity: "cycling", Source: "Garmin Connect", Start: start.Add(time.Minute), End: start.Add(81 * time.Minute)}
	if _, err := rig.st.UpsertWorkouts(ctx, a, []model.Workout{mirror}); err != nil {
		t.Fatal(err)
	}
	body := []byte(fmt.Sprintf(`{"activities":[{"userId":"fixture-athlete","activityId":777,"startTimeInSeconds":%d,
		"durationInSeconds":4800,"activityType":"cycling","activeKilocalories":1300}]}`, start.Unix()))
	deliverAs := func(provider string, body []byte, mediaType string, sign bool, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/"+provider, bytes.NewReader(body))
		req.Header.Set("Content-Type", mediaType)
		if sign {
			mac := hmac.New(sha256.New, []byte(secret))
			_, _ = mac.Write(body)
			req.Header.Set("X-HMAC-SHA256-Signature", hex.EncodeToString(mac.Sum(nil)))
		}
		resp := httptest.NewRecorder()
		rig.srv.ServeHTTP(resp, req)
		if resp.Code != want {
			t.Fatalf("webhook: %d %s", resp.Code, resp.Body)
		}
	}
	deliver := func(provider string, body []byte, sign bool, want int) {
		t.Helper()
		deliverAs(provider, body, "application/json", sign, want)
	}
	deliver("garmin", body, false, 401)
	deliver("garmin", body, true, 200)
	deliver("garmin", body, true, 200)
	suuntoBody := []byte(`{"username":"fixture-suunto","workoutid":"fit-777"}`)
	deliver("suunto", suuntoBody, true, 200)
	suuntoForm := []byte("username=fixture-suunto&workoutid=fit-777")
	deliver("suunto", suuntoForm, true, 422)
	deliverAs("suunto", suuntoBody, "application/x-www-form-urlencoded", true, 422)
	deliverAs("suunto", suuntoForm, "application/x-www-form-urlencoded", true, 200)
	var n int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM nutrition.wearable_inbox`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("enqueue replay: %d %v", n, err)
	}
	if _, err := rig.pool.Exec(ctx, `UPDATE nutrition.wearable_inbox SET due_at=CASE provider
		WHEN 'suunto' THEN $1::timestamptz - interval '1 minute' ELSE $1 END`, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	h.partners.secrets["suunto"] = ""
	deliver("suunto", suuntoBody, true, 404)
	if err := h.processWearables(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM nutrition.wearable_inbox
		WHERE (provider='suunto' AND attempts=0 AND completed_at IS NULL)
		   OR (provider='garmin' AND attempts=1 AND completed_at IS NOT NULL)`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("disabled Suunto delivery should remain unclaimed while Garmin completes: %d %v", n, err)
	}
	h.partners.secrets["suunto"] = secret
	if err := h.processWearables(ctx); err != nil {
		t.Fatal(err)
	}
	var out model.CoachingBriefing
	check(a, http.MethodGet, "/v1/coaching/daily-briefing", nil, 200, &out)
	expected := energy.Workout(wearables.Workout{Provider: wearables.SuuntoFIT, Sport: "cycling", WorkKJ: 725, Start: start, End: start.Add(80 * time.Minute)},
		energy.Physiology{MassKG: 70, RMR: energy.MifflinStJeor(70, 178, 36, "male")}).NetKcal
	if out.Plan == nil || out.Plan.SessionClass != periodization.Moderate || math.Abs(out.Plan.NetExercise-expected) > .00001 ||
		out.N2R == nil || out.N2R.Budget.EnergyTarget != out.Plan.EnergyKcal || out.N2R.Budget.CarbTarget != out.Plan.CarbsG {
		t.Fatalf("briefing: %+v plan: %+v expected exercise: %v", out, out.Plan, expected)
	}
	enabled := func(id uuid.UUID) bool { return id == a }
	if err := rig.st.RefreshCoaching(ctx, now, nutritionPolicy, enabled); err != nil {
		t.Fatal(err)
	}
	now = now.AddDate(0, 0, 1)
	if err := rig.st.RefreshCoaching(ctx, now, nutritionPolicy, enabled); err != nil {
		t.Fatal(err)
	}
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM nutrition.model_evaluation_logs WHERE user_id=$1 AND workout_source='SUUNTO_FIT'`, a).Scan(&n); err != nil || n != 1 {
		t.Fatalf("model evaluation: %d %v", n, err)
	}
	result, err := rig.st.CoachingCohorts(ctx, "2026-11-01")
	if err != nil || len(result) != 0 {
		t.Fatalf("one account must never surface in cohorts: %+v %v", result, err)
	}
	// Disabling every callback must keep retention running without processing
	// a disabled delivery or disturbing a fresh replay marker.
	if _, err := rig.pool.Exec(ctx, `UPDATE nutrition.wearable_inbox SET completed_at=NULL,
		due_at=$1,body='{"fixture":"expired"}'::jsonb WHERE provider='suunto'`, now.AddDate(0, 0, -8)); err != nil {
		t.Fatal(err)
	}
	h.partners.secrets = nil
	deliver("garmin", body, true, 404)
	deliver("suunto", suuntoBody, true, 404)
	if err := h.processWearables(ctx); err != nil {
		t.Fatal(err)
	}
	var expired, retained int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE provider='suunto'),
		count(*) FILTER (WHERE provider='garmin' AND completed_at IS NOT NULL)
		FROM nutrition.wearable_inbox`).Scan(&expired, &retained); err != nil || expired != 0 || retained != 1 {
		t.Fatalf("disabled inbox retention: expired=%d retained=%d, error %v", expired, retained, err)
	}
	check(a, http.MethodDelete, "/v1/users/goal-profile", nil, 204, nil)
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM nutrition.model_evaluation_logs WHERE user_id=$1`, a).Scan(&n); err != nil || n != 0 {
		t.Fatalf("opt-out leaves evaluation data: %d %v", n, err)
	}
}
