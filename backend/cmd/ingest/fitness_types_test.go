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

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

func TestDropGated(t *testing.T) {
	streams := []string{model.SampleBasalEnergy, model.SampleHeartRateRecovery, model.SampleBodyMass, model.SampleSteps}
	for _, tc := range []struct {
		name           string
		pilot, fitness bool
		keep           []bool // in streams order
	}{
		{"neither", false, false, []bool{false, false, false, true}},
		{"pilot", true, false, []bool{false, false, true, true}},
		{"fitness", false, true, []bool{true, true, true, true}},
		{"both", true, true, []bool{true, true, true, true}},
	} {
		for i, typ := range streams {
			t.Run(tc.name+"/"+typ, func(t *testing.T) {
				cursor := &model.HealthKitCursor{Stream: typ, Sequence: 1, Value: "synthetic"}
				req := model.SyncRequest{
					Samples: []model.Sample{{Type: model.SampleHeartRate}, {Type: typ}},
					Cursor:  cursor,
				}
				dropped := dropGated(&req, tc.pilot, tc.fitness)
				if tc.keep[i] {
					if dropped != 0 || len(req.Samples) != 2 || req.Samples[1].Type != typ || req.Cursor != cursor {
						t.Fatal("eligible sample and cursor should both stay")
					}
				} else if dropped != 1 || len(req.Samples) != 1 || req.Cursor != nil {
					t.Fatal("ineligible sample and cursor should both be dropped")
				}
				if req.Samples[0].Type != model.SampleHeartRate {
					t.Fatal("ordinary sample changed")
				}
			})
		}
	}
}

// A direct connection never stores the D304 types: they are gated on sync only (D306).
func TestConnectionsRefuseGatedTypes(t *testing.T) {
	now := time.Now()
	c := &connectHook{now: func() time.Time { return now }}
	at := now.Add(-time.Hour)
	for typ, want := range map[string]string{model.SampleBasalEnergy: "parse", model.SampleHeartRateRecovery: "parse",
		model.SampleBodyMass: "parse", model.SampleHeartRate: ""} {
		rec := connect.Records{Samples: []model.Sample{{Type: typ, Start: at, End: at, Value: 1}}}
		if _, code := c.screenRecord(store.InboxItem{}, rec, time.UTC); code != want {
			t.Errorf("%s: code %q, want %q", typ, code, want)
		}
	}
}

func TestSyncFitnessTypes(t *testing.T) {
	pool := testPool(t)
	ingestPool, _ := foldRolePool(t, pool, "ryft_ingest")
	ctx := context.Background()
	const namedPolicy = "test-named-policy"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &handler{st: store.New(ingestPool), log: log, outbox: outbox.New(ingestPool, "ingest", nil, log),
		beta:         config.Beta{Gate: config.ModeOff, Consent: config.ModeOff, PrivacyPolicyVersion: namedPolicy},
		namesFitness: func(version string) bool { return version == namedPolicy }}
	const apiKey = "fitness-sync-test-0123456789"
	srv := newMux(h, log, auth.NewAPIKey(apiKey, uuid.New()), http.NotFoundHandler())

	admin := store.New(pool)
	putFlag := func(t *testing.T, enabled bool, users ...uuid.UUID) {
		t.Helper()
		flag := model.FeatureFlag{Key: model.FlagSyncFitnessTypes, Enabled: enabled, AllowUserIDs: users}
		if _, _, err := admin.PutFlag(ctx, flag); err != nil {
			t.Fatal(err)
		}
	}
	grant := func(t *testing.T, user model.User, policy string) {
		t.Helper()
		if err := admin.GrantConsent(ctx, user.ID, policy, nil); err != nil {
			t.Fatal(err)
		}
	}
	post := func(t *testing.T, user model.User, payload model.SyncRequest) model.SyncResponse {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/sync", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+apiKey+":"+user.ID.String())
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /v1/sync returned %d, want 200", rec.Code)
		}
		var out model.SyncResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	for _, tc := range []struct {
		name, policy string
		flag, allow  bool
	}{
		{"flag off", namedPolicy, false, false},
		{"policy does not name types", "test-old-policy", true, false},
		{"flag and named consent", namedPolicy, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &delEnv{t: t, pool: pool, st: admin}
			user := e.newUser()
			grant(t, user, tc.policy)
			putFlag(t, tc.flag, user.ID)
			at := base()
			basalID, recoveryID, massID, stepsID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			req := model.SyncRequest{DeviceID: uuid.New(), Samples: []model.Sample{
				{Type: model.SampleBasalEnergy, ID: &basalID, Start: at, End: at, Value: 60, Unit: "kcal", Source: "Watch"},
				{Type: model.SampleHeartRateRecovery, ID: &recoveryID, Start: at, End: at, Value: 40, Unit: "bpm", Source: "Watch"},
				{Type: model.SampleBodyMass, ID: &massID, Start: at, End: at, Value: 70, Unit: "kg", Source: "Scale", WasUserEntered: true},
				{Type: model.SampleSteps, ID: &stepsID, Start: at, End: at, Value: 10, Unit: "count", Source: "Watch"},
			}, Cursor: &model.HealthKitCursor{Stream: model.SampleBasalEnergy, Sequence: 1, Value: "synthetic"}}
			wantStored, wantIgnored := 0, 3
			if tc.allow {
				wantStored, wantIgnored = 1, 0
			}
			out := post(t, user, req)
			if out.Accepted.Samples != 1+3*wantStored || out.Ignored.Samples.UnknownType != wantIgnored {
				t.Fatalf("accepted %d samples, ignored %d unknown", out.Accepted.Samples, out.Ignored.Samples.UnknownType)
			}
			for _, id := range []uuid.UUID{basalID, recoveryID, massID} {
				if got := e.hasSample(user, id); got != wantStored {
					t.Errorf("optional sample count = %d, want %d", got, wantStored)
				}
			}
			if e.hasSample(user, stepsID) != 1 {
				t.Error("ordinary sample was lost")
			}
			const cursorCount = `SELECT count(*) FROM nutrition.wearable_sync_cursors WHERE user_id=$1 AND device_id=$2 AND stream=$3`
			if got := e.count(cursorCount, user.ID, req.DeviceID, model.SampleBasalEnergy); got != wantStored {
				t.Errorf("basal energy cursor count = %d, want %d", got, wantStored)
			}
			cursorOnly := model.SyncRequest{DeviceID: req.DeviceID,
				Cursor: &model.HealthKitCursor{Stream: model.SampleHeartRateRecovery, Sequence: 1, Value: "synthetic"}}
			post(t, user, cursorOnly)
			if got := e.count(cursorCount, user.ID, req.DeviceID, model.SampleHeartRateRecovery); got != wantStored {
				t.Errorf("recovery cursor count = %d, want %d", got, wantStored)
			}

			if !tc.allow {
				return
			}
			repeat := post(t, user, req)
			if repeat.Accepted.Samples != 0 || repeat.Ignored.Samples.Duplicate != len(req.Samples) {
				t.Fatalf("replay accepted %d and counted %d duplicates", repeat.Accepted.Samples, repeat.Ignored.Samples.Duplicate)
			}
			other := e.newUser()
			grant(t, other, namedPolicy)
			otherReq := post(t, other, req)
			if otherReq.Accepted.Samples != 1 || otherReq.Ignored.Samples.UnknownType != 3 {
				t.Fatal("the allow-list or deduplication affected another account")
			}
			deletion := model.SyncRequest{DeviceID: req.DeviceID,
				Deleted: []model.DeletedRecord{{Kind: model.DeletedSample, ID: recoveryID}}}
			if got := post(t, other, deletion); got.Deleted.Samples != 0 || got.Ignored.Samples.UnknownID != 1 ||
				e.hasSample(user, recoveryID) != 1 || e.hasSample(other, recoveryID) != 0 {
				t.Fatal("the second account could see or delete the caller's sample")
			}
			putFlag(t, false, user.ID)
			if got := post(t, user, deletion); got.Deleted.Samples != 1 || e.hasSample(user, recoveryID) != 0 {
				t.Fatal("the kill switch blocked deletion of a stored fitness sample")
			}
		})
	}
}
