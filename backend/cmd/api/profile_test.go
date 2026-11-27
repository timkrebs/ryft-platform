package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

const profilePath = "/v1/me"

func newProfileRig(t *testing.T, now time.Time, names func(string) bool) *nutritionRig {
	t.Helper()
	return newNutritionRig(t, func(h *handler) {
		h.cache = newInsightCache()
		h.beta.Consent = config.ModeOff
		h.profileClock = func() time.Time { return now }
		h.profilePolicyNames = names
	})
}

func profileResponse(t *testing.T, rig *nutritionRig, user uuid.UUID, method, body string, want int) map[string]any {
	t.Helper()
	resp := rig.do(user, method, profilePath, body)
	if resp.Code != want {
		t.Fatalf("%s %s: got %d, want %d: %s", method, profilePath, resp.Code, want, resp.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func assertProfileFields(t *testing.T, got, want map[string]any) {
	t.Helper()
	for key, value := range want {
		if actual, ok := got[key]; !ok || actual != value {
			t.Errorf("%s: got %v (present=%v), want %v", key, actual, ok, value)
		}
	}
}

func assertProfileWeight(t *testing.T, got map[string]any, kg float64, source string, at time.Time) {
	t.Helper()
	want := map[string]any{"weight_kg": nil, "weight_source": nil, "weight_measured_at": nil}
	if source != "" {
		want["weight_kg"], want["weight_source"], want["weight_measured_at"] = kg, source, at.UTC().Format(time.RFC3339Nano)
	}
	assertProfileFields(t, got, want)
}

func TestProfilePatchValidation(t *testing.T) {
	h := &handler{beta: config.Beta{Consent: config.ModeOff, PrivacyPolicyVersion: nutritionPolicy}}
	user := model.User{Consent: &model.Consent{PolicyVersion: nutritionPolicy}}
	for _, tt := range []struct {
		name, body string
		status     int
	}{
		{"unknown goal", `{"goal":"x"}`, 422},
		{"unknown sex", `{"sex":"other"}`, 422},
		{"low weight", `{"weight_kg":24.9}`, 422},
		{"high weight", `{"weight_kg":350.1}`, 422},
		{"weight string", `{"weight_kg":"64"}`, 400},
		{"weight overflow", `{"weight_kg":1e400}`, 400},
		{"sex number", `{"sex":1}`, 400},
		{"goal object", `{"goal":{}}`, 400},
		{"read-only source", `{"weight_source":"manual"}`, 400},
		{"read-only time", `{"weight_measured_at":null}`, 400},
		{"sex requires consent", `{"sex":"female"}`, 403},
		{"weight requires consent", `{"weight_kg":64.5}`, 403},
		{"clearing requires consent", `{"weight_kg":null}`, 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPatch, profilePath, strings.NewReader(tt.body))
			resp := httptest.NewRecorder()
			h.patchMe(resp, r.WithContext(auth.WithUser(r.Context(), user)))
			if resp.Code != tt.status {
				t.Fatalf("got %d, want %d: %s", resp.Code, tt.status, resp.Body)
			}
			if tt.status == 403 && errorCode(t, resp.Body.Bytes()) != model.CodeConsentRequired {
				t.Errorf("expected consent_required: %s", resp.Body)
			}
		})
	}
}

func TestProfileErrKeepsNoValue(t *testing.T) {
	pg := &pgconn.PgError{Code: "23514", ConstraintName: "users_weight_manual_kg_check",
		Message: "new row violates check constraint", Detail: "Failing row contains (64.5, female)."}
	got := profileErr(fmt.Errorf("update: %w", pg)).Error()
	if !strings.Contains(got, "23514") || !strings.Contains(got, "users_weight_manual_kg_check") {
		t.Errorf("lost the diagnosis: %s", got)
	}
	for _, leak := range []string{"64.5", "female", "Failing row"} {
		if strings.Contains(got, leak) {
			t.Errorf("logs %q: %s", leak, got)
		}
	}
}

func TestProfileGoalAndDefaultConsent(t *testing.T) {
	rig := newProfileRig(t, time.Now(), nil)
	newUser, consented := rig.account(true), rig.account()
	got := profileResponse(t, rig, newUser, http.MethodGet, "", 200)
	assertProfileFields(t, got, map[string]any{"goal": nil, "sex": nil})
	assertProfileWeight(t, got, 0, "", time.Time{})
	for _, tt := range []struct {
		body string
		goal any
	}{
		{`{"goal":"strength"}`, "strength"},
		{`{"tz":"UTC"}`, "strength"},
		{`{"goal":null}`, nil},
	} {
		got = profileResponse(t, rig, newUser, http.MethodPatch, tt.body, 200)
		assertProfileFields(t, got, map[string]any{"goal": tt.goal})
	}
	for _, body := range []string{
		`{"sex":"female"}`, `{"sex":null}`, `{"weight_kg":64.5}`, `{"weight_kg":null}`,
		`{"goal":"strength","sex":"female","tz":"UTC"}`,
	} {
		got = profileResponse(t, rig, consented, http.MethodPatch, body, 403)
		assertProfileFields(t, got, map[string]any{"code": model.CodeConsentRequired})
	}
	got = profileResponse(t, rig, consented, http.MethodGet, "", 200)
	assertProfileFields(t, got, map[string]any{"goal": nil, "tz": "Europe/Berlin"})
}

func TestProfileBodyPatch(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 15, 3, 567000000, time.FixedZone("CET", 3600))
	rig := newProfileRig(t, now, func(version string) bool { return version == nutritionPolicy })
	user := rig.account()
	got := profileResponse(t, rig, user, http.MethodPatch, `{"sex":"female","weight_kg":64.5}`, 200)
	assertProfileFields(t, got, map[string]any{"sex": "female"})
	assertProfileWeight(t, got, 64.5, model.WeightSourceManual, now.Truncate(time.Second))
	got = profileResponse(t, rig, user, http.MethodPatch, `{"sex":null}`, 200)
	assertProfileFields(t, got, map[string]any{"sex": nil})
	assertProfileWeight(t, got, 64.5, model.WeightSourceManual, now.Truncate(time.Second))
	got = profileResponse(t, rig, user, http.MethodPatch, `{"weight_kg":64.45}`, 200)
	assertProfileWeight(t, got, 64.5, model.WeightSourceManual, now.Truncate(time.Second))
}

func TestProfileHealthKitWeight(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	rig := newProfileRig(t, now, func(version string) bool { return version == nutritionPolicy })
	user, other := rig.account(), rig.account()
	flag := func(enabled bool) {
		t.Helper()
		if _, _, err := rig.st.PutFlag(ctx, model.FeatureFlag{Key: model.FlagSyncFitnessTypes, Enabled: enabled, AllowUserIDs: []uuid.UUID{user}}); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(owner uuid.UUID, at time.Time, kg float64) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := rig.st.UpsertSamples(ctx, owner, []model.Sample{{ID: &id, Type: model.SampleBodyMass,
			Start: at, End: at, Value: kg, Unit: "kg", Source: "Scale"}}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	remove := func(id uuid.UUID) {
		t.Helper()
		if _, err := rig.st.ApplySync(ctx, user, store.Apply{Loc: time.UTC, Deleted: []model.DeletedRecord{{Kind: model.DeletedSample, ID: id}}}); err != nil {
			t.Fatal(err)
		}
	}
	get := func() map[string]any {
		t.Helper()
		return profileResponse(t, rig, user, http.MethodGet, "", 200)
	}
	got := profileResponse(t, rig, user, http.MethodPatch, `{"weight_kg":64.5}`, 200)
	assertProfileWeight(t, got, 64.5, model.WeightSourceManual, now)
	insert(other, now.Add(90*time.Minute), 94)
	insert(user, now.Add(3*time.Hour), 90) // future measurement
	if _, err := rig.pool.Exec(ctx, `INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source, origin, vendor, record_id)
		VALUES ($1, 'body_mass', $2, $2, 88, 'kg', 'ryft-direct-garmin', 'direct', 'garmin', 'sample:profile-test')`, user, now.Add(80*time.Minute)); err != nil {
		t.Fatal(err)
	}
	flag(true)
	assertProfileWeight(t, get(), 64.5, model.WeightSourceManual, now)
	older, newer := now.Add(15*time.Minute), now.Add(45*time.Minute)
	olderID, newerID := insert(user, older, 70.24), insert(user, newer, 78.25)
	assertProfileWeight(t, get(), 78.3, model.WeightSourceHealthKit, newer)
	flag(false)
	assertProfileWeight(t, get(), 64.5, model.WeightSourceManual, now)
	flag(true)
	if err := rig.st.GrantConsent(ctx, user, "unlisted-version", nil); err != nil {
		t.Fatal(err)
	}
	assertProfileWeight(t, get(), 64.5, model.WeightSourceManual, now)
	if err := rig.st.RevokeConsent(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := rig.st.GrantConsent(ctx, user, nutritionPolicy, nil); err != nil {
		t.Fatal(err)
	}
	remove(newerID)
	assertProfileWeight(t, get(), 70.2, model.WeightSourceHealthKit, older)
	remove(olderID)
	assertProfileWeight(t, get(), 64.5, model.WeightSourceManual, now)
	olderID = insert(user, now.Add(-time.Hour), 70.24)
	assertProfileWeight(t, get(), 64.5, model.WeightSourceManual, now)
	got = profileResponse(t, rig, user, http.MethodPatch, `{"weight_kg":null}`, 200)
	assertProfileWeight(t, got, 70.2, model.WeightSourceHealthKit, now.Add(-time.Hour))
	remove(olderID)
	assertProfileWeight(t, get(), 0, "", time.Time{})
}

func TestProfileWriteRefusesWithdrawnConsent(t *testing.T) {
	var rig *nutritionRig
	var user uuid.UUID
	checked := false
	// Withdraw on the first policy check, just after the handler has received
	// the previously active version. Its check still admits the patch.
	rig = newProfileRig(t, time.Now(), func(version string) bool {
		if !checked {
			checked = true
			if err := rig.st.RevokeConsent(context.Background(), user); err != nil {
				t.Fatal(err)
			}
		}
		return version == nutritionPolicy
	})
	user = rig.account()
	got := profileResponse(t, rig, user, http.MethodPatch, `{"goal":"strength","sex":"female","weight_kg":64.5,"tz":"UTC"}`, 403)
	assertProfileFields(t, got, map[string]any{"code": model.CodeConsentRequired})
	got = profileResponse(t, rig, user, http.MethodGet, "", 200)
	assertProfileFields(t, got, map[string]any{"goal": nil, "sex": nil, "tz": "Europe/Berlin"})
	assertProfileWeight(t, got, 0, "", time.Time{})
}
