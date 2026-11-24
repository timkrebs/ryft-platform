package wearables

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPartnerFetchBindsAccountAndWorkout(t *testing.T) {
	ctx := context.Background()
	queued := Delivery{User: "linked", RemoteID: "123"}
	validActivity := `{"userId":"linked","activityId":123,"startTimeInSeconds":1795507200,"durationInSeconds":3600,"activityType":"cycling"}`
	otherActivity := strings.ReplaceAll(validActivity, "linked", "other")
	wrongWorkout := strings.ReplaceAll(validActivity, "123", "456")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" || r.URL.Query().Get("provider_user") != queued.User ||
			r.URL.Query().Get("workout_id") != queued.RemoteID {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/garmin":
			fmt.Fprintf(w, `{"activityUpdates":[%s,%s,%s]}`, otherActivity, wrongWorkout, validActivity)
		case "/suunto":
			fmt.Fprint(w, `{"provider_user":"linked","activity":[{"at":"2026-11-24T14:05:00Z","active_kcal":22}]}`)
		case "/wrong":
			fmt.Fprint(w, `{"provider_user":"other","activity":[{"at":"2026-11-24T14:05:00Z","active_kcal":22}]}`)
		case "/waiting":
			fmt.Fprintf(w, `{"activities":[%s]}`, wrongWorkout)
		case "/redirect":
			http.Redirect(w, r, "/suunto", http.StatusFound)
		case "/malformed":
			fmt.Fprint(w, "{")
		case "/fit":
			w.Header().Set("Content-Type", "application/octet-stream")
			fmt.Fprint(w, "invalid FIT")
		}
	}))
	defer srv.Close()

	for _, tc := range []struct {
		kind, endpoint, wantErr    string
		wantWorkouts, wantActivity int
	}{
		{kind: "garmin", endpoint: "/garmin", wantWorkouts: 1},
		{kind: "suunto", endpoint: "/suunto", wantActivity: 1},
		{kind: "suunto", endpoint: "/wrong", wantErr: "unexpected connector response"},
		{kind: "garmin", endpoint: "/waiting", wantErr: "partner data not ready"},
		{kind: "suunto", endpoint: "/redirect", wantErr: "HTTP 302"},
		{kind: "suunto", endpoint: "/malformed", wantErr: "unexpected end of JSON"},
		{kind: "suunto", endpoint: "/fit", wantErr: "not a FIT file"},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			f, err := NewFetchers(srv.URL+tc.endpoint, srv.URL+tc.endpoint)
			if err != nil {
				t.Fatal(err)
			}
			out, err := f.Fetch(ctx, tc.kind, queued, "fixture", srv.Client().Transport)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil || len(out.Workouts) != tc.wantWorkouts || len(out.Activity) != tc.wantActivity {
				t.Fatalf("fetch: %+v %v", out, err)
			}
		})
	}
	if _, err := NewFetchers("http://localhost/fit", ""); err == nil {
		t.Fatal("accepted insecure connector")
	}
	f, err := NewFetchers("", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Fetch(ctx, "garmin", queued, "fixture", nil); err == nil {
		t.Fatal("accepted queued job without a configured connector")
	}
	if out, err := f.Fetch(ctx, "garmin", Delivery{User: queued.User}, "", nil); err != nil || out.User != queued.User {
		t.Fatalf("push without a remote artifact: %+v %v", out, err)
	}
}

func TestGarminHealthNormalizationAndActivitySlots(t *testing.T) {
	body := []byte(`{
		"bodyComps":[{"userId":"linked","startTimeInSeconds":1795507200,"weightInGrams":70340},{"userId":"linked","startTimeInSeconds":1795507200,"weightInGrams":20}],
		"sleeps":[{"userId":"linked","startTimeInSeconds":1795507200,"durationInSeconds":28800,"deepSleepDurationInSeconds":4200,"remSleepInSeconds":5400}],
		"hrv":[{"userId":"linked","startTimeInSeconds":1795507200,"lastNightAvg":44}],
		"dailies":[{"userId":"linked","startTimeInSeconds":1795507200},{"userId":"","startTimeInSeconds":1795507200}],
		"activities":[{"userId":"linked","startTimeInSeconds":1795507200}]
	}`)
	got, err := DecodeDelivery("garmin", body)
	if err != nil || len(got) != 3 || *got[0].Nights[0].Deep != 70 || *got[0].Nights[0].REM != 90 ||
		got[0].Nights[0].WakeAt.Unix() != 1795536000 || *got[1].Nights[0].RMSSD != 44 || got[2].Weights[0].Mass != 70.34 {
		t.Fatalf("Garmin normalized measurements: %+v %v", got, err)
	}
	for _, tc := range []struct{ provider, body string }{
		{"unknown", "{}"}, {"garmin", "["}, {"suunto", `{"workoutid":32}`}, {"suunto", "%"},
		{"suunto", `{"workoutid":false}`},
	} {
		if _, err := DecodeDelivery(tc.provider, []byte(tc.body)); err == nil {
			t.Fatalf("accepted invalid %s input: %q", tc.provider, tc.body)
		}
	}
	at := time.Date(2026, 11, 24, 8, 1, 0, 0, time.FixedZone("device", 3600))
	slots := TenMinuteActivity([]ActivitySample{
		{At: at, Kcal: 12}, {At: at.UTC(), Kcal: 24}, {At: at.Add(time.Minute), Kcal: 4},
		{At: at.Add(2 * time.Minute), Kcal: -1}, {At: at.Add(10 * time.Minute), Kcal: 21}, {},
	})
	if len(slots) != 2 || slots[at.UTC().Truncate(10*time.Minute)] != 16 || slots[at.UTC().Truncate(10*time.Minute).Add(10*time.Minute)] != 21 {
		t.Fatalf("deduplicated UTC slots: %+v", slots)
	}
	if Signed(body, base64.StdEncoding.EncodeToString([]byte("invalid digest")), "fixture") || Signed(body, "@@", "fixture") || Signed(body, "", "") {
		t.Fatal("accepted invalid signature")
	}
}
