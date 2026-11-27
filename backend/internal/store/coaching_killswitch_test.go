package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/periodization"
)

// TestFrozenPlanDoesNotOutliveTheKillSwitch pins D295 for the coaching plan saved by the coaching tick: a check-in's
// readiness moves the plan of a day while recovery.checkin is on; once the flag is off, the day's historical N2R and
// its stored night must equal those of an account that never had the flag on.
//
// Two accounts with the same data: a (the flag on at the capture, then off) and b (never on). Both have a check-in
// on D with an unexplained drop and no overnight HRV, so the HRV path is UNKNOWN and, with the flag, the check-in
// gives LOW_RECOVERY_UNEXPLAINED: the protective plan raises the rest day's carbohydrate from 3.5 to 4.5 g/kg.
func TestFrozenPlanDoesNotOutliveTheKillSwitch(t *testing.T) {
	st, root, a, b := nutritionTestStore(t)
	ctx := context.Background()
	const policy = "2026-10-26"
	const dDay, wDay = "2026-11-20", "2026-11-21"
	capturedAt := utc(dDay + "T20:00:00Z")
	later := utc(wDay + "T10:00:00Z")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := root.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []uuid.UUID{a, b} {
		exec(`UPDATE users SET birth_year=1990,tz='UTC' WHERE id=$1`, u)
		if err := st.GrantConsent(ctx, u, policy, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutN2RProfile(ctx, u, model.N2RProfile{BodyMassKG: 70, ExpectedBedtime: "23:00"}); err != nil {
			t.Fatal(err)
		}
		// No body fat: availability stays UNKNOWN, so only the readiness can move the plan.
		if _, err := st.PutCoachingGoal(ctx, u, model.GoalProfile{Goal: periodization.FatLoss, HeightCM: 172}, utc("2026-11-17T08:00:00Z")); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO recovery_checkins(user_id,wake_date,bedtime,wake_time,tz_offset_sec,perceived_sleep_quality,perceived_freshness,
			soreness,sleep_duration_min,recovery_score,components,acute_drop,drop_reason,model_version)
			VALUES($1,$2::date,$3,$4,0,2,2,false,300,25,'{}'::jsonb,true,'checkin_low_quality_and_freshness','test.1')`,
			u, dDay, utc("2026-11-20T01:00:00Z"), utc("2026-11-20T06:00:00Z"))
		// The morning after D has a resting heart rate (no HRV), so the stored night of W exists.
		exec(`INSERT INTO daily_scores(user_id,day,rhr) VALUES($1,$2::date,55)`, u, wDay)
	}
	recoveryFlag(t, New(root), a)

	type dayView struct {
		plan model.CoachingPlan
		n2r  model.N2RScore
	}
	view := func(u uuid.UUID, asOf time.Time, recovery bool) dayView {
		t.Helper()
		var out dayView
		err := st.nutritionTx(ctx, u, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			p, err := readN2RProfile(ctx, tx, u, false)
			if err != nil || p == nil {
				t.Fatalf("n2r profile: %v %v", p, err)
			}
			h, err := readN2RHistoryWith(ctx, tx, u, *p, dDay, dDay, asOf, recovery)
			if err != nil {
				return err
			}
			state, ok := h.Coaching.plans[dDay]
			if !ok {
				t.Fatalf("no coaching plan for %s", dDay)
			}
			out = dayView{plan: state.Plan, n2r: h.n2r(dDay, true)}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// The check-in moves the live plan of D (else the test proves nothing).
	shown, off := view(a, capturedAt, true), view(a, capturedAt, false)
	if shown.plan.CarbsG <= off.plan.CarbsG || reflect.DeepEqual(shown.plan, off.plan) {
		t.Fatalf("the check-in did not move the plan: with %+v, without %+v", shown.plan, off.plan)
	}

	for _, u := range []uuid.UUID{a, b} {
		if err := st.captureCoachingDay(ctx, u, dDay, capturedAt, policy); err != nil {
			t.Fatal(err)
		}
	}
	// With the flag still on, a's saved day keeps the plan a was shown.
	if got := view(a, later, true); !reflect.DeepEqual(got.plan, shown.plan) {
		t.Fatalf("flag on: the saved plan is %+v, want the one shown %+v", got.plan, shown.plan)
	}

	// The kill switch.
	if _, _, err := New(root).PutFlag(ctx, model.FeatureFlag{Key: model.FlagRecoveryCheckin, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if on, err := st.RecoveryEnabled(ctx, a); err != nil || on {
		t.Fatalf("flag still on for a: %v %v", on, err)
	}

	// Historical N2R of D, flag off (what readN2RHistory decides from the flag).
	gotA, gotB := view(a, later, false), view(b, later, false)
	// (Carbohydrate against the live plan without the check-in: the tick's calibration may move the energy target.)
	if !reflect.DeepEqual(gotA.plan, gotB.plan) || gotA.plan.CarbsG != off.plan.CarbsG {
		t.Fatalf("historical plan after the kill switch: a %+v, never on %+v, live without the check-in %+v", gotA.plan, gotB.plan, off.plan)
	}
	if !reflect.DeepEqual(gotA.n2r, gotB.n2r) {
		t.Fatalf("historical N2R after the kill switch: a %+v, never on %+v", gotA.n2r, gotB.n2r)
	}

	// The stored night of W (its intake day is D): the same for both accounts.
	stored := map[uuid.UUID]string{}
	for _, u := range []uuid.UUID{a, b} {
		if err := st.refreshN2RNight(ctx, dueNight{UserID: u, Date: wDay}, later, policy); err != nil {
			t.Fatal(err)
		}
		var breakdown string
		if err := root.QueryRow(ctx, `SELECT n2r_score_breakdown::text FROM nutrition.daily_biometric_baselines
			WHERE user_id=$1 AND wake_date=$2::date`, u, wDay).Scan(&breakdown); err != nil {
			t.Fatal(err)
		}
		stored[u] = breakdown
	}
	if stored[a] != stored[b] {
		t.Fatalf("stored night after the kill switch: a %s, never on %s", stored[a], stored[b])
	}

	// The saved projection (evaluation and calibration input) never carried the check-in either.
	var projA, projB string
	if err := root.QueryRow(ctx, `SELECT n2r_projection::text FROM nutrition.daily_energy_states WHERE user_id=$1 AND day=$2::date`, a, dDay).Scan(&projA); err != nil {
		t.Fatal(err)
	}
	if err := root.QueryRow(ctx, `SELECT n2r_projection::text FROM nutrition.daily_energy_states WHERE user_id=$1 AND day=$2::date`, b, dDay).Scan(&projB); err != nil {
		t.Fatal(err)
	}
	if projA != projB {
		t.Fatalf("saved projection: a %s, never on %s", projA, projB)
	}
}
