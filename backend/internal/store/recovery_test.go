package store

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/train"
)

// Postgres tests of the recovery-without-a-watch store (D291–D295, D298, D299): check-ins and their rescoring, the wearable
// night and the tiers, the N2R bedtime precedence, and the session rating's life with its workout. The score itself
// is internal/scoring's (the store takes it as a CheckinScorer, as scoring imports the store): these tests use a
// simple stand-in whose result depends on the history the way the real one does. The real numbers are pinned
// through the api (cmd/api/recovery_test.go).

// fakeScorer scores a check-in as ten times its quality plus the quality of every check-in of the 14 days before,
// so editing or deleting one moves the later ones.
func fakeScorer(f CheckinFacts) CheckinResult {
	sum := f.Report.Quality * 10
	for _, p := range f.Prior {
		sum += p[0]
	}
	return CheckinResult{SleepDurationMin: int(f.Report.WakeTime.Sub(f.Report.Bedtime) / time.Minute), RecoveryScore: min(100, sum),
		Components: model.RecoveryComponents{Wellness: sum, WellnessAbsolute: sum, Duration: 100}, ModelVersion: "test.1"}
}

func recoveryUserUTC(t *testing.T, st *Store, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	u := applyUser(t, st)
	if _, err := pool.Exec(context.Background(), `UPDATE users SET tz='UTC' WHERE id=$1`, u); err != nil {
		t.Fatal(err)
	}
	return u
}

func recoveryFlag(t *testing.T, st *Store, users ...uuid.UUID) {
	t.Helper()
	if _, _, err := st.PutFlag(context.Background(), model.FeatureFlag{Key: model.FlagRecoveryCheckin, Enabled: true, AllowUserIDs: users}); err != nil {
		t.Fatal(err)
	}
}

// report is a night that ends on the wake day at 06:00 UTC after 8 hours, rated quality/5.
func report(wakeDay string, quality int) CheckinReport {
	wake := utc(wakeDay + "T06:00:00Z")
	return CheckinReport{Bedtime: wake.Add(-8 * time.Hour), WakeTime: wake, Quality: quality, Freshness: 5}
}

func TestCheckinIsAnIdempotentUpsertAndRescoresTheLaterOnes(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := recoveryUserUTC(t, st, pool)
	now := utc("2026-11-26T10:00:00Z")
	put := func(day string, quality int) model.RecoveryCheckin {
		t.Helper()
		out, err := st.PutCheckin(ctx, u, day, report(day, quality), now, fakeScorer)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	score := func(day string) int {
		t.Helper()
		c, err := st.Checkin(ctx, u, time.UTC, day)
		if err != nil {
			t.Fatal(err)
		}
		return c.RecoveryScore
	}
	first, second := put("2026-11-24", 5), put("2026-11-25", 5)
	if first.RecoveryScore != 50 || second.RecoveryScore != 55 || second.SleepDurationMin != 480 || second.Confidence != model.RecoveryConfidenceLow ||
		second.Superseded || second.N2RBedtimeApplied || second.Confounders == nil {
		t.Fatalf("saved: %+v %+v", first, second)
	}

	// The same report again: the same check-in; created_at and updated_at stay (updated_at is the report's).
	if _, err := pool.Exec(ctx, `UPDATE recovery_checkins SET updated_at = updated_at - interval '1 hour' WHERE user_id=$1`, u); err != nil {
		t.Fatal(err)
	}
	before, err := st.Checkin(ctx, u, time.UTC, "2026-11-25")
	if err != nil {
		t.Fatal(err)
	}
	again := put("2026-11-25", 5)
	if !again.CreatedAt.Equal(before.CreatedAt) || !again.UpdatedAt.Equal(before.UpdatedAt) || again.RecoveryScore != 55 {
		t.Fatalf("an identical report changed the check-in: %+v → %+v", before, again)
	}
	changed := put("2026-11-25", 6)
	if !changed.CreatedAt.Equal(before.CreatedAt) || !changed.UpdatedAt.After(before.UpdatedAt) || changed.RecoveryScore != 65 {
		t.Fatalf("a different report: created %v updated %v → %v, score %d", before.CreatedAt, before.UpdatedAt, changed.UpdatedAt, changed.RecoveryScore)
	}

	// Editing an earlier check-in rescores the later one (not its updated_at: the report did not change); so does
	// deleting it, also when there was none to delete.
	if _, err := pool.Exec(ctx, `UPDATE recovery_checkins SET updated_at = updated_at - interval '1 hour' WHERE user_id=$1`, u); err != nil {
		t.Fatal(err)
	}
	stamp, _ := st.Checkin(ctx, u, time.UTC, "2026-11-25")
	put("2026-11-24", 8)
	if got := score("2026-11-25"); got != 68 {
		t.Fatalf("the later check-in after an edit: %d, want 68", got)
	}
	if moved, _ := st.Checkin(ctx, u, time.UTC, "2026-11-25"); !moved.UpdatedAt.Equal(stamp.UpdatedAt) {
		t.Fatal("a rescore moved updated_at")
	}
	if err := st.DeleteCheckin(ctx, u, "2026-11-24", now, fakeScorer); err != nil {
		t.Fatal(err)
	}
	if got := score("2026-11-25"); got != 60 {
		t.Fatalf("the later check-in after a delete: %d, want 60", got)
	}
	if err := st.DeleteCheckin(ctx, u, "2026-11-24", now, fakeScorer); err != nil {
		t.Fatalf("deleting none: %v", err)
	}
	if _, err := st.Checkin(ctx, u, time.UTC, "2026-11-24"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted check-in: %v", err)
	}

	// The range: oldest first, an empty one is [] not null, and one account never reads another's.
	list, err := st.Checkins(ctx, u, time.UTC, "2026-11-01", "2026-11-30")
	if err != nil || len(list) != 1 || list[0].Date != "2026-11-25" {
		t.Fatalf("range: %+v %v", list, err)
	}
	other := recoveryUserUTC(t, st, pool)
	if list, err := st.Checkins(ctx, other, time.UTC, "2026-11-01", "2026-11-30"); err != nil || list == nil || len(list) != 0 {
		t.Fatalf("another account's range: %#v %v", list, err)
	}

	// A model bump: a row another version computed is rescored by the next call.
	if _, err := pool.Exec(ctx, `UPDATE recovery_checkins SET model_version='old', recovery_score=1 WHERE user_id=$1`, u); err != nil {
		t.Fatal(err)
	}
	if n, err := st.RescoreStaleCheckins(ctx, u, "test.1", now, fakeScorer); err != nil || n != 1 {
		t.Fatalf("stale rescore: %d %v", n, err)
	}
	if c, _ := st.Checkin(ctx, u, time.UTC, "2026-11-25"); c.ModelVersion != "test.1" || c.RecoveryScore != 60 {
		t.Fatalf("after the model bump: %+v", c)
	}
	if n, err := st.RescoreStaleCheckins(ctx, u, "test.1", now, fakeScorer); err != nil || n != 0 {
		t.Fatalf("nothing stale: %d %v", n, err)
	}
}

// seedNight stores what scoring would for the night ending on day: its recovery, and asleep stages or an overnight HRV.
func seedNight(t *testing.T, pool *pgxpool.Pool, u uuid.UUID, day string, recovery *int, stages bool, hrv *float64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO daily_scores (user_id, day, recovery, sleep_has_stages, hrv_ms) VALUES ($1, $2::date, $3, $4, $5)
		ON CONFLICT (user_id, day) DO UPDATE SET recovery = EXCLUDED.recovery, sleep_has_stages = EXCLUDED.sleep_has_stages, hrv_ms = EXCLUDED.hrv_ms`,
		u, day, recovery, stages, hrv); err != nil {
		t.Fatal(err)
	}
}

// seedHeartRate writes one heart-rate sample at each of n hours from from.
func seedHeartRate(t *testing.T, pool *pgxpool.Pool, u uuid.UUID, from time.Time, n int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source)
		SELECT $1, 'heart_rate', t, t, 62, 'bpm', 'Watch' FROM generate_series($2::timestamptz, $2::timestamptz + ($3 - 1) * interval '1 hour', interval '1 hour') t`,
		u, from, n); err != nil {
		t.Fatal(err)
	}
}

// The wearable night is one rule (D291): asleep stages or an overnight HRV, and a recovery. It decides the status,
// the tier of a check-in, whether to ask and whether a check-in is superseded.
func TestRecoveryStatusFollowsTheWearableNight(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := recoveryUserUTC(t, st, pool)
	recoveryFlag(t, st, u)
	const day = "2026-11-26"
	at := func(hm string) time.Time { return utc(day + "T" + hm + ":00Z") }
	status := func(now time.Time) model.RecoveryStatus {
		t.Helper()
		s, err := st.RecoveryStatus(ctx, u, now)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	put := func() model.RecoveryCheckin {
		t.Helper()
		out, err := st.PutCheckin(ctx, u, day, report(day, 7), at("10:00"), fakeScorer)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Nothing: 03:59 does not ask, 04:00 does.
	if s := status(at("03:59")); s.Source != model.RecoverySourceNone || s.NeedsCheckin || s.Confidence != nil || s.RecoveryScore != nil || s.Checkin != nil || s.Date != day {
		t.Fatalf("03:59: %+v", s)
	}
	if s := status(at("04:00")); s.Source != model.RecoverySourceNone || !s.NeedsCheckin || s.SuggestionBasis != nil {
		t.Fatalf("04:00: %+v", s)
	}
	// An in_bed-only night (the iPhone's) is not a wearable night, whatever else the day has: no stages, no HRV.
	seedNight(t, pool, u, day, ip(55), false, nil)
	if s := status(at("10:00")); s.Source != model.RecoverySourceNone || !s.NeedsCheckin {
		t.Fatalf("a night without stages or HRV: %+v", s)
	}
	// The stages come from sleep_segments, whatever scoring flagged: an inbed segment in the night window is not a
	// wearable night, an asleep one is (older Garmin firmware has no core, deep or rem), and one ending at 13:00 or
	// later is outside the window.
	segment := func(stage, end string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source) VALUES ($1, $2::timestamptz - interval '7 hours', $2, $3, 'Watch')`,
			u, utc(end), stage); err != nil {
			t.Fatal(err)
		}
	}
	segment("inbed", "2026-11-26T06:00:00Z")
	segment("asleep", "2026-11-26T13:30:00Z")
	if s := status(at("10:00")); s.Source != model.RecoverySourceNone || !s.NeedsCheckin {
		t.Fatalf("an inbed segment, and an asleep one past the night window: %+v", s)
	}
	segment("asleep", "2026-11-26T05:30:00Z")
	if s := status(at("10:00")); s.Source != model.RecoverySourceWearable || s.NeedsCheckin {
		t.Fatalf("asleep stages in the night window with a recovery: %+v", s)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM sleep_segments WHERE user_id=$1`, u); err != nil {
		t.Fatal(err)
	}
	// A day watch: resting heart rate gives a recovery, there is no night. The check-in is the day's estimate, medium
	// with the day before's heart rate in 8 hours, low with 7, and is not superseded.
	seedHeartRate(t, pool, u, utc("2026-11-25T08:00:00Z"), 7)
	checkin := put()
	if checkin.Confidence != model.RecoveryConfidenceLow || checkin.Superseded {
		t.Fatalf("7 hours of heart rate: %+v", checkin)
	}
	seedHeartRate(t, pool, u, utc("2026-11-25T15:00:00Z"), 1)
	checkin = put()
	if checkin.Confidence != model.RecoveryConfidenceMedium || checkin.Superseded {
		t.Fatalf("8 hours of heart rate: %+v", checkin)
	}
	if s := status(at("10:00")); s.Source != model.RecoverySourceCheckin || s.Confidence == nil || *s.Confidence != model.RecoveryConfidenceMedium ||
		s.RecoveryScore == nil || *s.RecoveryScore != checkin.RecoveryScore || s.NeedsCheckin || s.Checkin == nil {
		t.Fatalf("day watch and a check-in: %+v", s)
	}
	// Watch-recorded stages and a recovery: a wearable night. It supersedes the check-in, which stays readable.
	seedNight(t, pool, u, day, ip(71), true, nil)
	s := status(at("10:00"))
	if s.Source != model.RecoverySourceWearable || s.Confidence == nil || *s.Confidence != model.RecoveryConfidenceHigh ||
		s.RecoveryScore == nil || *s.RecoveryScore != 71 || s.NeedsCheckin || s.Checkin == nil || !s.Checkin.Superseded {
		t.Fatalf("wearable night: %+v", s)
	}
	if list, _ := st.Checkins(ctx, u, time.UTC, day, day); len(list) != 1 || !list[0].Superseded {
		t.Fatalf("a superseded check-in stays in the range read: %+v", list)
	}
	// An overnight HRV with a recovery is one too, and a stage night without a recovery is not.
	seedNight(t, pool, u, day, ip(66), false, ptrFloat(48))
	if s := status(at("10:00")); s.Source != model.RecoverySourceWearable {
		t.Fatalf("an overnight HRV: %+v", s)
	}
	seedNight(t, pool, u, day, nil, true, nil)
	if s := status(at("10:00")); s.Source != model.RecoverySourceCheckin || s.Checkin == nil || s.Checkin.Superseded {
		t.Fatalf("a night of stages without a recovery: %+v", s)
	}
	// Deleting the wearable data makes the check-in current again; deleting the check-in asks again.
	if _, err := pool.Exec(ctx, `DELETE FROM daily_scores WHERE user_id=$1`, u); err != nil {
		t.Fatal(err)
	}
	if s := status(at("10:00")); s.Source != model.RecoverySourceCheckin || s.Checkin == nil || s.Checkin.Superseded {
		t.Fatalf("after the wearable data went: %+v", s)
	}
	if err := st.DeleteCheckin(ctx, u, day, at("10:00"), fakeScorer); err != nil {
		t.Fatal(err)
	}
	if s := status(at("10:00")); s.Source != model.RecoverySourceNone || !s.NeedsCheckin {
		t.Fatalf("after the check-in went: %+v", s)
	}
}

func ptrFloat(v float64) *float64 { return &v }

// The suggestion pre-fills the form: the median of the last 7 days' check-ins from 3 of them, else the N2R expected
// bedtime and 8 hours after it, else nothing.
func TestRecoveryStatusSuggestsTimes(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := recoveryUserUTC(t, st, pool)
	now := utc("2026-11-26T10:00:00Z")
	status := func() model.RecoveryStatus {
		t.Helper()
		s, err := st.RecoveryStatus(ctx, u, now)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := status(); s.SuggestedBedtime != nil || s.SuggestedWakeTime != nil || s.SuggestionBasis != nil {
		t.Fatalf("no basis: %+v", s)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO nutrition.n2r_profiles (user_id, body_mass_kg, sex, expected_bedtime) VALUES ($1, 70, 'unspecified', '22:45')`, u); err != nil {
		t.Fatal(err)
	}
	s := status()
	if s.SuggestionBasis == nil || *s.SuggestionBasis != model.RecoveryBasisExpectedBedtime || *s.SuggestedBedtime != "22:45" || *s.SuggestedWakeTime != "06:45" {
		t.Fatalf("expected bedtime: %+v", s)
	}
	// Two check-ins are not enough, three are: bed 22:30, 23:30 and 00:40 (the night after), wake 06:00, 06:30, 07:00.
	put := func(day, bed, wake string) {
		t.Helper()
		_, err := st.PutCheckin(ctx, u, day, CheckinReport{Bedtime: utc(bed), WakeTime: utc(wake), Quality: 7, Freshness: 6}, now, fakeScorer)
		if err != nil {
			t.Fatal(err)
		}
	}
	put("2026-11-23", "2026-11-22T22:30:00Z", "2026-11-23T06:00:00Z")
	put("2026-11-24", "2026-11-23T23:30:00Z", "2026-11-24T06:30:00Z")
	if s := status(); *s.SuggestionBasis != model.RecoveryBasisExpectedBedtime {
		t.Fatalf("two check-ins: %+v", s)
	}
	put("2026-11-25", "2026-11-25T00:40:00Z", "2026-11-25T07:00:00Z")
	s = status()
	if *s.SuggestionBasis != model.RecoveryBasisCheckins7d || *s.SuggestedBedtime != "23:30" || *s.SuggestedWakeTime != "06:30" {
		t.Fatalf("median of three: %+v", s)
	}
}

// The bedtime N2R reads (D293): the night's measured sleep start, then the next morning's check-in while the flag is
// on, then (added by the caller) the expected one.
func TestNutritionBedtimePrecedence(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := recoveryUserUTC(t, st, pool)
	now := utc("2026-11-26T10:00:00Z")
	bedtimes := func() (map[string]*time.Time, map[string]string) {
		t.Helper()
		var at map[string]*time.Time
		var src map[string]string
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			on, err := recoveryOn(ctx, tx, u) // N2R's read: with the flag
			if err != nil {
				return err
			}
			at, src, err = nutritionBedtimesWith(ctx, tx, u, "2026-11-24", "2026-11-24", on)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return at, src
	}
	// A check-in for the morning of the 25th: bed 22:00 on the 24th.
	rep := CheckinReport{Bedtime: utc("2026-11-24T22:00:00Z"), WakeTime: utc("2026-11-25T06:00:00Z"), Quality: 6, Freshness: 6}
	if _, err := st.PutCheckin(ctx, u, "2026-11-25", rep, now, fakeScorer); err != nil {
		t.Fatal(err)
	}
	if at, _ := bedtimes(); len(at) != 0 {
		t.Fatalf("flag off: the check-in counts nowhere: %v", at)
	}
	recoveryFlag(t, st, u)
	at, src := bedtimes()
	if got := at["2026-11-24"]; got == nil || !got.Equal(rep.Bedtime) || src["2026-11-24"] != model.N2RBedtimeCheckin {
		t.Fatalf("a check-in's bedtime: %v %v", at, src)
	}
	// The meal-timing score of the nutrition diary (scoreNutritionDays) reads measured sleep only: N2R alone takes it.
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		diary, _, err := nutritionBedtimesWith(ctx, tx, u, "2026-11-24", "2026-11-24", false)
		if len(diary) != 0 {
			t.Errorf("the diary's bedtimes hold a check-in's: %v", diary)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A measured sleep start beats it.
	if _, err := pool.Exec(ctx, `INSERT INTO daily_scores (user_id, day, sleep_start, sleep_actual_min) VALUES ($1, '2026-11-25', $2, 400)`,
		u, utc("2026-11-24T23:30:00Z")); err != nil {
		t.Fatal(err)
	}
	at, src = bedtimes()
	if got := at["2026-11-24"]; got == nil || !got.Equal(utc("2026-11-24T23:30:00Z")) || src["2026-11-24"] != model.N2RBedtimeWearable {
		t.Fatalf("a measured sleep start: %v %v", at, src)
	}
	// Without it, and without the check-in, nothing: the profile's expected bedtime is the caller's fallback.
	if _, err := pool.Exec(ctx, `DELETE FROM daily_scores WHERE user_id=$1`, u); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteCheckin(ctx, u, "2026-11-25", now, fakeScorer); err != nil {
		t.Fatal(err)
	}
	if at, _ := bedtimes(); len(at) != 0 {
		t.Fatalf("after the delete: %v", at)
	}
}

func rate(t *testing.T, pool *pgxpool.Pool, u, workout uuid.UUID, rpe float64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO workout_ratings (user_id, workout_id, session_rpe) VALUES ($1, $2, $3::float8::numeric(3,1))`, u, workout, rpe); err != nil {
		t.Fatal(err)
	}
}

func ratings(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) map[uuid.UUID]float64 {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT workout_id, session_rpe::float8 FROM workout_ratings WHERE user_id=$1`, u)
	if err != nil {
		t.Fatal(err)
	}
	out := map[uuid.UUID]float64{}
	for rows.Next() {
		var id uuid.UUID
		var v float64
		if err := rows.Scan(&id, &v); err != nil {
			t.Fatal(err)
		}
		out[id] = v
	}
	rows.Close()
	return out
}

func TestWorkoutRatingLifeAndOwnership(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u, other := applyUser(t, st), applyUser(t, st)
	at := utc("2026-11-25T17:00:00Z")
	own := seedWorkout(t, pool, u, seedW{source: "Watch", start: at})
	theirs := seedWorkout(t, pool, other, seedW{source: "Watch", start: at})

	w, err := st.PutWorkoutRating(ctx, u, own, 8)
	if err != nil || w.SessionRPE == nil || *w.SessionRPE != 8 || w.SessionRPELoad == nil || *w.SessionRPELoad != 480 || w.ID != own {
		t.Fatalf("rate: %+v %v", w, err)
	}
	// It replaces; the list reads show it; another account's workout is not found, for a write and a delete.
	if w, err = st.PutWorkoutRating(ctx, u, own, 6.5); err != nil || *w.SessionRPE != 6.5 || *w.SessionRPELoad != 390 {
		t.Fatalf("replace: %+v %v", w, err)
	}
	list, err := st.ListWorkoutsBetween(ctx, u, at.Add(-time.Hour), at.Add(time.Hour), false)
	if err != nil || len(list) != 1 || list[0].SessionRPE == nil || *list[0].SessionRPE != 6.5 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if plain, _ := st.VisibleWorkoutsBetween(ctx, u, at.Add(-time.Hour), at.Add(time.Hour)); len(plain) != 1 || plain[0].SessionRPE != nil {
		t.Fatalf("the rating-free read (scoring's role cannot read the value): %+v", plain)
	}
	if rated, _ := st.RatedWorkoutsBetween(ctx, u, at.Add(-time.Hour), at.Add(time.Hour)); len(rated) != 1 || rated[0].SessionRPE == nil {
		t.Fatalf("rated read: %+v", rated)
	}
	if row, err := st.Workout(ctx, u, own); err != nil || row.SessionRPE == nil || *row.SessionRPE != 6.5 {
		t.Fatalf("the workout: %+v %v", row, err)
	}
	if _, err := st.PutWorkoutRating(ctx, u, theirs, 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rating another account's workout: %v", err)
	}
	if _, err := st.PutWorkoutRating(ctx, u, uuid.New(), 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rating an unknown workout: %v", err)
	}
	if err := st.DeleteWorkoutRating(ctx, u, theirs); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a rating of another account's workout: %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM workout_ratings WHERE user_id=$1`, other); n != 0 {
		t.Fatalf("another account got a rating: %d", n)
	}

	// A hidden duplicate cannot be rated: rate the primary.
	dup := seedWorkout(t, pool, u, seedW{source: "Garmin", start: at.Add(time.Minute)})
	if _, err := pool.Exec(ctx, `UPDATE workouts SET duplicate_of=$2 WHERE user_id=$1 AND id=$3`, u, own, dup); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutWorkoutRating(ctx, u, dup, 5); !errors.Is(err, ErrHiddenDuplicate) {
		t.Fatalf("a hidden duplicate: %v", err)
	}

	// A sync of the same workout (same id, another end) keeps the rating; deleting the rating of an unrated workout
	// is fine; a HealthKit deletion of the workout takes its rating.
	if _, err := st.UpsertWorkouts(ctx, u, []model.Workout{{ID: own, Activity: "running", Start: at, End: at.Add(2 * time.Hour), Source: "Watch"}}); err != nil {
		t.Fatal(err)
	}
	if got := ratings(t, pool, u); got[own] != 6.5 {
		t.Fatalf("a re-sync lost the rating: %v", got)
	}
	if err := st.DeleteWorkoutRating(ctx, u, dup); err != nil {
		t.Fatalf("deleting the rating of an unrated workout: %v", err)
	}
	if _, err := st.ApplySync(ctx, u, Apply{Loc: berlin, Deleted: []model.DeletedRecord{del(model.DeletedWorkout, own)}}); err != nil {
		t.Fatal(err)
	}
	if got := ratings(t, pool, u); len(got) != 0 {
		t.Fatalf("a HealthKit deletion left the rating: %v", got)
	}
}

// The same-source fold runs in ingest's sync: it moves a dropped copy's rating to the copy that stays before it
// deletes the copy, through the grants ingest has (it may repoint a rating, never write a value).
func TestFoldMovesTheRatingToTheSurvivor(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	ingest := New(dupRolePool(t, pool, "ryft_ingest"))
	at := utc("2026-01-12T10:00:00Z")
	scope := foldScope("Garmin", at)
	for _, c := range []struct {
		name                    string
		oldRPE, midRPE, keptRPE float64 // 0: unrated
		want                    float64
	}{
		{"the survivor is unrated", 8, 0, 0, 8},
		{"the survivor has its own", 8, 0, 4, 4},
		{"two dropped copies rated: the latest rating", 8, 6, 0, 6},
		{"nothing rated", 0, 0, 0, 0},
	} {
		u := applyUser(t, st)
		old := seedWorkout(t, pool, u, seedW{source: "Garmin", start: at, created: at.Add(-2 * time.Hour)})
		mid := seedWorkout(t, pool, u, seedW{source: "Garmin", start: at.Add(time.Second), created: at.Add(-time.Hour)})
		kept := seedWorkout(t, pool, u, seedW{source: "Garmin", start: at.Add(2 * time.Second), created: at})
		for _, r := range []struct {
			id  uuid.UUID
			rpe float64
		}{{old, c.oldRPE}, {mid, c.midRPE}, {kept, c.keptRPE}} {
			if r.rpe > 0 {
				rate(t, pool, u, r.id, r.rpe)
				time.Sleep(5 * time.Millisecond) // updated_at orders the dropped copies' ratings
			}
		}
		out, err := ingest.ApplySync(ctx, u, Apply{Loc: berlin, Fold: scope})
		if err != nil || out.Folded != 2 {
			t.Fatalf("%s: fold %+v %v", c.name, out, err)
		}
		got := ratings(t, pool, u)
		if c.want == 0 {
			if len(got) != 0 {
				t.Fatalf("%s: ratings %v", c.name, got)
			}
			continue
		}
		if len(got) != 1 || got[kept] != c.want {
			t.Fatalf("%s: ratings %v, want %v on the survivor", c.name, got, c.want)
		}
	}
}

// A rating belongs to the session's workout, wherever scoring attaches the session: from its own workout to a
// HealthKit one, from one HealthKit workout to another, and back to its own. The move runs as ryft_scoring (key
// columns only); the target keeps a rating of its own.
func TestReleaseSessionWorkoutsMovesTheRating(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	scoring := New(dupRolePool(t, pool, "ryft_scoring"))
	at := utc("2026-11-25T17:00:00Z")
	for _, c := range []struct {
		name     string
		previous string // the workout the session was attached to: own, hk1 or "" (none)
		chosen   string // own or hk2
		rated    map[string]float64
		want     map[string]float64
	}{
		{"its own workout to a HealthKit one", "own", "hk2", map[string]float64{"own": 8}, map[string]float64{"hk2": 8}},
		{"its own workout to a HealthKit one that is rated", "own", "hk2", map[string]float64{"own": 8, "hk2": 5}, map[string]float64{"hk2": 5}},
		{"one HealthKit workout to another", "hk1", "hk2", map[string]float64{"hk1": 8}, map[string]float64{"hk2": 8}},
		{"one HealthKit workout to another that is rated", "hk1", "hk2", map[string]float64{"hk1": 8, "hk2": 5}, map[string]float64{"hk1": 8, "hk2": 5}},
		{"a HealthKit workout back to its own", "hk1", "own", map[string]float64{"hk1": 8}, map[string]float64{"own": 8}},
		// the workout it had wins over the session's own, which goes with its rating
		{"a rating on the workout it had and on its own", "hk1", "hk2", map[string]float64{"hk1": 8, "own": 4}, map[string]float64{"hk2": 8}},
	} {
		u := applyUser(t, st)
		session := uuid.New()
		ids := map[string]uuid.UUID{"own": train.SessionWorkoutID(u, session),
			"hk1": seedWorkout(t, pool, u, seedW{source: "Watch", start: at}), "hk2": seedWorkout(t, pool, u, seedW{source: "Garmin", start: at})}
		// The session is completed and has its own workout (as EnsureSessionWorkout leaves it).
		if _, err := pool.Exec(ctx, `INSERT INTO workouts (id, user_id, activity, start_at, end_at, source, session_id) VALUES ($1, $2, 'strength', $3, $3::timestamptz + interval '1 h', $4, $5)`,
			ids["own"], u, at, model.SessionSource, session); err != nil {
			t.Fatal(err)
		}
		var previous *uuid.UUID
		if id, ok := ids[c.previous]; ok {
			previous = &id
		}
		if _, err := pool.Exec(ctx, `INSERT INTO sessions (user_id, id, started_at, ended_at, status, workout_id) VALUES ($1, $2, $3, $3::timestamptz + interval '1 h', 'completed', $4)`,
			u, session, at, previous); err != nil {
			t.Fatal(err)
		}
		for name, rpe := range c.rated {
			rate(t, pool, u, ids[name], rpe)
		}
		prev := map[uuid.UUID]uuid.UUID{}
		if previous != nil {
			prev[session] = *previous
		}
		if err := scoring.ReleaseSessionWorkouts(ctx, u, []uuid.UUID{session}, map[uuid.UUID]uuid.UUID{session: ids[c.chosen]}, prev); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := ratings(t, pool, u)
		want := map[uuid.UUID]float64{}
		for name, rpe := range c.want {
			want[ids[name]] = rpe
		}
		if !maps.Equal(got, want) {
			t.Errorf("%s: ratings %v, want %v", c.name, got, want)
		}
		owns := countRows(t, pool, `SELECT count(*) FROM workouts WHERE user_id=$1 AND id=$2`, u, ids["own"])
		if keeps := c.chosen == "own"; keeps && owns != 1 || !keeps && owns != 0 {
			t.Errorf("%s: the session's own workout: %d rows", c.name, owns)
		}
	}
}

// A pass that failed after SetSessionLoad saved the new attachment runs again with that attachment as the previous
// one: the rating still on the workout that carries the session moves to the chosen one.
func TestReleaseSessionWorkoutsRetryMovesTheRating(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	scoring := New(dupRolePool(t, pool, "ryft_scoring"))
	u := applyUser(t, st)
	session := uuid.New()
	at := utc("2026-11-25T17:00:00Z")
	hk1 := seedWorkout(t, pool, u, seedW{source: "Watch", start: at})
	hk2 := seedWorkout(t, pool, u, seedW{source: "Garmin", start: at})
	if _, err := pool.Exec(ctx, `UPDATE workouts SET session_id=$2 WHERE user_id=$1 AND id=$3`, u, session, hk1); err != nil { // the first attachment
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sessions (user_id, id, started_at, ended_at, status, workout_id) VALUES ($1, $2, $3, $3::timestamptz + interval '1 h', 'completed', $4)`,
		u, session, at, hk2); err != nil { // SetSessionLoad already saved the new one
		t.Fatal(err)
	}
	rate(t, pool, u, hk1, 8)
	chosen := map[uuid.UUID]uuid.UUID{session: hk2}
	for i := 0; i < 2; i++ { // and again: the move is idempotent
		if err := scoring.ReleaseSessionWorkouts(ctx, u, []uuid.UUID{session}, chosen, chosen); err != nil {
			t.Fatal(err)
		}
		if got := ratings(t, pool, u); len(got) != 1 || got[hk2] != 8 {
			t.Fatalf("pass %d: ratings %v, want 8 on the chosen workout", i+1, got)
		}
	}
}

// A completed session whose times are edited keeps its workout (and with it the rating): scoring updates it in place
// instead of deleting it and inserting it again.
func TestSessionWorkoutTimeEditKeepsTheRating(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	scoring := New(dupRolePool(t, pool, "ryft_scoring"))
	u := applyUser(t, st)
	session := uuid.New()
	own := train.SessionWorkoutID(u, session)
	at := utc("2026-11-25T17:00:00Z")
	if err := scoring.EnsureSessionWorkout(ctx, u, own, session, at, at.Add(time.Hour), 42); err != nil {
		t.Fatal(err)
	}
	rate(t, pool, u, own, 7)
	if _, err := pool.Exec(ctx, `UPDATE workouts SET strain=9, trimp=80, model_version='x' WHERE user_id=$1 AND id=$2`, u, own); err != nil {
		t.Fatal(err)
	}
	// The same times again change nothing; new times and load follow in place.
	if err := scoring.EnsureSessionWorkout(ctx, u, own, session, at, at.Add(time.Hour), 42); err != nil {
		t.Fatal(err)
	}
	if err := scoring.EnsureSessionWorkout(ctx, u, own, session, at.Add(10*time.Minute), at.Add(50*time.Minute), 30); err != nil {
		t.Fatal(err)
	}
	var start, end time.Time
	var load float64
	var strain *float64
	if err := pool.QueryRow(ctx, `SELECT start_at, end_at, muscular_load, strain FROM workouts WHERE user_id=$1 AND id=$2`, u, own).Scan(&start, &end, &load, &strain); err != nil {
		t.Fatal(err)
	}
	if !start.Equal(at.Add(10*time.Minute)) || !end.Equal(at.Add(50*time.Minute)) || load != 30 || strain != nil {
		t.Fatalf("the edited session workout: %v %v load %v strain %v (the day's pass scores it again)", start, end, load, strain)
	}
	if got := ratings(t, pool, u); got[own] != 7 || len(got) != 1 {
		t.Fatalf("a time edit lost the rating: %v", got)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM workouts WHERE user_id=$1`, u); n != 1 {
		t.Fatalf("workouts: %d", n)
	}
}

// What deletion and merge do with both tables (the owned() count of delete_test.go covers the erasure of every row).
func TestRecoveryRowsFollowAMerge(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	from, to := applyUser(t, st), applyUser(t, st)
	at := utc("2026-11-25T17:00:00Z")
	moving := seedWorkout(t, pool, from, seedW{source: "Watch", start: at})
	rate(t, pool, from, moving, 7)
	now := utc("2026-11-26T10:00:00Z")
	for _, c := range []struct {
		user uuid.UUID
		day  string
	}{{from, "2026-11-24"}, {from, "2026-11-25"}, {to, "2026-11-25"}} {
		if _, err := st.PutCheckin(ctx, c.user, c.day, report(c.day, 6), now, fakeScorer); err != nil {
			t.Fatal(err)
		}
	}
	res, err := st.MergeUsers(ctx, from, to, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]int64{}
	for _, tb := range res.Tables {
		got[tb.Table] = [2]int64{tb.Moved, tb.Dropped}
	}
	if got["workout_ratings"] != [2]int64{1, 0} || got["recovery_checkins"] != [2]int64{1, 1} {
		t.Fatalf("merge tables: %v", got)
	}
	if r := ratings(t, pool, to); r[moving] != 7 || len(ratings(t, pool, from)) != 0 {
		t.Fatalf("the rating follows its workout: to %v from %v", r, ratings(t, pool, from))
	}
	list, err := st.Checkins(ctx, to, time.UTC, "2026-11-01", "2026-11-30")
	if err != nil || len(list) != 2 || !slices.Equal([]string{list[0].Date, list[1].Date}, []string{"2026-11-24", "2026-11-25"}) {
		t.Fatalf("the check-ins of the kept account: %+v %v", list, err)
	}
}

// A stored N2R night (its breakdown, the caffeine tuning from it) is computed with the profile's expected bedtime,
// never a check-in's, even with the flag on: it would outlive the kill switch (D293).
func TestStoredN2RNightIgnoresACheckinBedtime(t *testing.T) {
	st, root, a, _ := nutritionTestStore(t)
	ctx := context.Background()
	const policy = "2026-10-01"
	now := time.Date(2026, 10, 19, 21, 0, 0, 0, time.UTC)
	if _, err := root.Exec(ctx, `UPDATE users SET tz='UTC' WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exec(ctx, `ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS hrv_metric text`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutN2RProfile(ctx, a, model.N2RProfile{BodyMassKG: 70, ExpectedBedtime: "23:00"}); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantConsent(ctx, a, policy, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= 7; i++ { // a week of HRV and no measured sleep, so no measured bedtime beats a check-in's
		hrv, rhr := 44+float64(i), 50.0
		if i == 0 {
			hrv, rhr = 30, 53
		}
		if _, err := root.Exec(ctx, `INSERT INTO daily_scores(user_id,day,hrv_ms,hrv_metric,rhr) VALUES($1,$2::date,$3,'rmssd',$4)`,
			a, now.AddDate(0, 0, -i).Format(time.DateOnly), hrv, rhr); err != nil {
			t.Fatal(err)
		}
	}
	recoveryFlag(t, st, a)
	rep := CheckinReport{Bedtime: utc("2026-10-18T21:30:00Z"), WakeTime: utc("2026-10-19T05:30:00Z"), Quality: 6, Freshness: 6}
	if _, err := st.PutCheckin(ctx, a, "2026-10-19", rep, now, fakeScorer); err != nil {
		t.Fatal(err)
	}
	if err := st.refreshN2RNight(ctx, dueNight{UserID: a, Date: "2026-10-19"}, now, policy); err != nil {
		t.Fatal(err)
	}
	var bedtime time.Time
	if err := root.QueryRow(ctx, `SELECT (n2r_score_breakdown->>'bedtime')::timestamptz FROM nutrition.daily_biometric_baselines
		WHERE user_id=$1 AND wake_date='2026-10-19'`, a).Scan(&bedtime); err != nil {
		t.Fatal(err)
	}
	if want := utc("2026-10-18T23:00:00Z"); !bedtime.Equal(want) {
		t.Fatalf("the stored night used bedtime %v, want the expected %v (the check-in's was %v)", bedtime, want, rep.Bedtime)
	}
}
