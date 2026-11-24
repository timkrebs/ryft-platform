package wearables

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"
)

func TestReconcileSensorPriorityAndOverlap(t *testing.T) {
	start := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	fit := Workout{Provider: GarminFIT, ExternalID: "1", Sport: "cycling", Start: start, End: start.Add(time.Hour), WorkKJ: 500}
	apple := Workout{Provider: HealthKit, ExternalID: "mirror", Sport: "biking", Start: start.Add(2 * time.Minute), End: start.Add(62 * time.Minute),
		Bundle: "com.garmin.connect.mobile", RawKcal: 1000}
	tail := Workout{Provider: Manual, ExternalID: "tail", Sport: "cycling", Start: start.Add(45 * time.Minute), End: start.Add(75 * time.Minute), RawKcal: 300}
	got := Reconcile([]Workout{apple, tail, fit})
	if len(got) != 2 || got[0].Provider != GarminFIT || got[0].WorkKJ != 500 || !got[1].Start.Equal(fit.End) || got[1].Seconds() != 900 || got[1].RawKcal != 150 {
		t.Fatalf("reconcile: %+v", got)
	}
}

func TestFITAndPartnerNotification(t *testing.T) {
	start := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	session := mesgdef.Session{StartTime: start, Timestamp: start.Add(time.Hour), Sport: typedef.SportCycling,
		TotalTimerTime: 3600000, TotalElapsedTime: 3600000, TotalWork: 600000, NormalizedPower: 220, ThresholdPower: 250}
	var sample bytes.Buffer
	if err := encoder.New(&sample).Encode(&proto.FIT{Messages: []proto.Message{session.ToMesg(nil)}}); err != nil {
		t.Fatal(err)
	}
	fit, err := DecodeFIT(context.Background(), "suunto", "session1", 3600, &sample)
	if err != nil || len(fit) != 1 || fit[0].WorkKJ != 600 || fit[0].NormalizedPower != 220 || fit[0].Priority() != 1 {
		t.Fatalf("FIT: %+v %v", fit, err)
	}
	body := []byte("username=suunto-account&workoutid=session1")
	secret := "fixture-token"
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	if !Signed(body, hex.EncodeToString(mac.Sum(nil)), secret) || Signed(body, "deadbeef", secret) {
		t.Fatal("signature check")
	}
	ds, err := DecodeDelivery("suunto", body)
	if err != nil || len(ds) != 1 || ds[0].RemoteID != "session1" {
		t.Fatalf("Suunto: %+v %v", ds, err)
	}
	garmin := []byte(`{"activities":[{"userId":"account","activityId":71,"startTimeInSeconds":1795507200,"durationInSeconds":900,"activityType":"cycling"}],"activityFiles":[{"userId":"account","activityId":71,"callbackURL":"https://unused.invalid/"}]}`)
	ds, err = DecodeDelivery("garmin", garmin)
	if err != nil || len(ds) != 2 || ds[0].Workouts[0].ExternalID != "71" || ds[1].RemoteID != "71" {
		t.Fatalf("Garmin: %+v %v", ds, err)
	}
}
