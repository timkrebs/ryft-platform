package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestHealthKitBodyMassRestrictedToProtectedPilot(t *testing.T) {
	e := newDelEnv(t)
	pilot, diary := e.user, e.newUser()
	ctx := context.Background()
	for _, f := range []model.FeatureFlag{
		{Key: model.FlagNutrition, Enabled: true, AllowUserIDs: []uuid.UUID{pilot.ID}},
		{Key: model.FlagNutritionDiary, Enabled: true, AllowUserIDs: []uuid.UUID{diary.ID}},
	} {
		if _, _, err := e.st.PutFlag(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	check := func(user model.User, allowed bool) {
		t.Helper()
		d := uuid.New()
		body, err := json.Marshal(map[string]any{
			"device_id": d,
			"samples": []map[string]any{
				{"type": "body_mass", "value": 70, "unit": "kg", "start": base(), "end": base(), "source": "Scale"},
				{"type": "steps", "value": 1, "unit": "count", "start": base(), "end": base(), "source": "Watch"},
			},
			"cursor": model.HealthKitCursor{Stream: model.SampleBodyMass, Sequence: 1, Value: "synthetic"},
		})
		if err != nil {
			t.Fatal(err)
		}
		r, out := send(e.serverFor(user), string(body))
		wantOptional, wantIgnored := 0, 1
		if allowed {
			wantOptional, wantIgnored = 1, 0
		}
		if r.Code != http.StatusOK || out.Accepted.Samples != 1+wantOptional || out.Ignored.Samples.UnknownType != wantIgnored {
			t.Fatalf("unrelated HealthKit streams affected or optional data persisted: status=%d, accepted=%d, ignored=%d",
				r.Code, out.Accepted.Samples, out.Ignored.Samples.UnknownType)
		}
		if got := e.count(`SELECT count(*) FROM samples WHERE user_id=$1 AND type='body_mass'`, user.ID); got != wantOptional {
			t.Fatalf("stored %d body mass readings, want %d", got, wantOptional)
		}
		if got := e.count(`SELECT count(*) FROM nutrition.wearable_sync_cursors WHERE user_id=$1 AND device_id=$2 AND stream='body_mass'`, user.ID, d); got != wantOptional {
			t.Fatalf("stored %d body mass cursors, want %d", got, wantOptional)
		}
	}
	check(diary, false)
	check(pilot, true)
}
