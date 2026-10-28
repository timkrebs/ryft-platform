package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

// D90: everything of from moves to to; where both have the same row, to's is
// kept; the dry run's counts are the real run's; from can then be deleted.
func TestMergeUsers(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	from, to := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{from, to} {
		if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q[:40], err)
		}
	}
	// Raw data: one sample both have, one only from has; a workout id both have.
	shared := model.Sample{Type: model.SampleHeartRate, Start: now.Add(-2 * time.Hour), End: now.Add(-2 * time.Hour), Value: 60, Source: "Watch"}
	own := model.Sample{Type: model.SampleSteps, Start: now.Add(-3 * time.Hour), End: now.Add(-3 * time.Hour), Value: 10, Source: "iPhone"}
	if _, err := st.UpsertSamples(ctx, from, []model.Sample{shared, own}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSamples(ctx, to, []model.Sample{shared}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSleep(ctx, from, []model.SleepSegment{{Start: now.Add(-9 * time.Hour), End: now.Add(-8 * time.Hour), Stage: "core", Source: "Watch"}}); err != nil {
		t.Fatal(err)
	}
	wkShared, wkOwn := uuid.New(), uuid.New()
	for _, w := range []struct {
		user, id uuid.UUID
	}{{from, wkShared}, {to, wkShared}, {from, wkOwn}} {
		if _, err := st.UpsertWorkouts(ctx, w.user, []model.Workout{{ID: w.id, Activity: "running", Start: now.Add(-5 * time.Hour),
			End: now.Add(-4 * time.Hour), Source: "Watch"}}); err != nil {
			t.Fatal(err)
		}
	}
	// One phone for both accounts, and a second device only from used.
	phone, tablet := uuid.New(), uuid.New()
	for _, d := range []struct{ user, id uuid.UUID }{{from, phone}, {to, phone}, {from, tablet}} {
		if err := st.TouchDevice(ctx, d.user, d.id, "x", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RegisterPushToken(ctx, from, tablet, model.PushRegistration{Token: hexToken(from), Env: "production", Locale: "de"}); err != nil {
		t.Fatal(err)
	}
	// Train: a custom exercise of the same slug on both, one only from has; a session
	// only from has (with the shared-slug exercise), and a session id both have.
	exFromPress, exToPress, exCurl := uuid.New(), uuid.New(), uuid.New()
	for _, e := range []struct {
		id, user uuid.UUID
		slug     string
	}{{exFromPress, from, "press"}, {exToPress, to, "press"}, {exCurl, from, "curl"}} {
		exec(`INSERT INTO exercises (id, user_id, slug, name_en, name_de, kind, primary_muscles, load_type)
			VALUES ($1, $2, $3, $3, $3, 'dumbbell', '{chest}', 'weight')`, e.id, e.user, e.slug)
	}
	sOwn, sShared := uuid.New(), uuid.New()
	session := func(user, id, ex uuid.UUID) {
		seID := uuid.New()
		exec(`INSERT INTO sessions (user_id, id, started_at, status) VALUES ($1, $2, $3, 'completed')`, user, id, now.Add(-30*time.Hour))
		exec(`INSERT INTO session_exercises (user_id, id, session_id, exercise_id, position) VALUES ($1, $2, $3, $4, 0)`, user, seID, id, ex)
		exec(`INSERT INTO session_sets (user_id, id, session_exercise_id, set_index, reps, weight_kg, updated_at)
			VALUES ($1, $2, $3, 0, 8, 20, now())`, user, uuid.New(), seID)
	}
	session(from, sOwn, exFromPress)
	session(from, sShared, exCurl)
	session(to, sShared, exToPress)
	// from's own session was imported (0028): origin, source and an assisted set move with it.
	exec(`UPDATE sessions SET origin = 'import', import_source = 'alpha_progression' WHERE user_id = $1 AND id = $2`, from, sOwn)
	exec(`UPDATE session_sets SET assist_kg = 25 WHERE user_id = $1 AND session_exercise_id IN
		(SELECT id FROM session_exercises WHERE user_id = $1 AND session_id = $2)`, from, sOwn)
	// Journal: a custom behavior of the same slug on both; a day only from has, a day both have.
	bFrom, bTo := uuid.New(), uuid.New()
	for _, b := range []struct{ id, user uuid.UUID }{{bFrom, from}, {bTo, to}} {
		exec(`INSERT INTO behaviors (id, user_id, slug, name_en, name_de, category, kind) VALUES ($1, $2, 'cold', 'Cold', 'Kalt', 'custom', 'yes_no')`, b.id, b.user)
	}
	dayOwn, dayShared := now.AddDate(0, 0, -3).Format("2006-01-02"), now.AddDate(0, 0, -2).Format("2006-01-02")
	for _, j := range []struct {
		user, behavior uuid.UUID
		day            string
	}{{from, bFrom, dayOwn}, {from, bFrom, dayShared}, {to, bTo, dayShared}} {
		exec(`INSERT INTO journal_days (user_id, day) VALUES ($1, $2)`, j.user, j.day)
		exec(`INSERT INTO journal_entries (user_id, day, behavior_id, value_bool) VALUES ($1, $2, $3, true)`, j.user, j.day, j.behavior)
	}
	if _, err := st.AddFeedback(ctx, from, model.FeedbackPost{Message: "from here"}, model.FeedbackOther); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSources(ctx, from, []SourceSeen{{Name: "Watch", Kind: model.KindHR, LastSeen: now}}); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantConsent(ctx, from, "2026-10-20", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBetaAccess(ctx, from, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBetaAccess(ctx, to, false); err != nil {
		t.Fatal(err)
	}

	byTable := func(r MergeResult) map[string][2]int64 {
		m := map[string][2]int64{}
		for _, x := range r.Tables {
			m[x.Table] = [2]int64{x.Moved, x.Dropped}
		}
		return m
	}
	want := map[string][2]int64{"samples": {1, 1}, "sleep_segments": {1, 0}, "workouts": {1, 1}, "devices": {1, 1},
		"push_tokens": {1, 0}, "exercises": {1, 1}, "sessions": {1, 1}, "behaviors": {0, 1}, "journal_days": {1, 1},
		"feedback": {1, 0}, "user_sources": {1, 0}}
	check := func(step string, r MergeResult) {
		t.Helper()
		got := byTable(r)
		for table, w := range want {
			if got[table] != w {
				t.Errorf("%s %s: moved/dropped %v, want %v", step, table, got[table], w)
			}
		}
		if r.Consent != "copied" || r.BetaAccess != "copied" || r.FirstRecord == nil || len(r.ExerciseIDs) != 1 {
			t.Errorf("%s: consent %s, beta %s, first %v, exercises %v", step, r.Consent, r.BetaAccess, r.FirstRecord, r.ExerciseIDs)
		}
	}
	dry, err := st.MergeUsers(ctx, from, to, true)
	if err != nil {
		t.Fatal(err)
	}
	check("dry run", dry)
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(`SELECT count(*) FROM samples WHERE user_id = $1`, to) != 1 || count(`SELECT count(*) FROM feedback WHERE user_id = $1`, to) != 0 {
		t.Fatal("a dry run changes nothing")
	}

	res, err := st.MergeUsers(ctx, from, to, false)
	if err != nil {
		t.Fatal(err)
	}
	check("merge", res)
	for _, c := range []struct {
		q    string
		args []any
		want int
	}{
		{`SELECT count(*) FROM samples WHERE user_id = $1`, []any{to}, 2},
		{`SELECT count(*) FROM workouts WHERE user_id = $1`, []any{to}, 2},
		{`SELECT count(*) FROM devices WHERE user_id = $1`, []any{to}, 2},
		{`SELECT count(*) FROM push_tokens WHERE user_id = $1`, []any{to}, 1},
		{`SELECT count(*) FROM sessions WHERE user_id = $1`, []any{to}, 2},
		{`SELECT count(*) FROM session_exercises WHERE user_id = $1`, []any{to}, 2},
		{`SELECT count(*) FROM session_sets WHERE user_id = $1`, []any{to}, 2},
		{`SELECT count(*) FROM exercises WHERE user_id = $1`, []any{to}, 2},
		{`SELECT count(*) FROM journal_days WHERE user_id = $1`, []any{to}, 2},
		{`SELECT count(*) FROM journal_entries WHERE user_id = $1`, []any{to}, 2},
		{`SELECT count(*) FROM consents WHERE user_id = $1 AND revoked_at IS NULL`, []any{to}, 1},
		{`SELECT count(*) FROM users WHERE id = $1 AND beta_access_at IS NOT NULL`, []any{to}, 1},
		{`SELECT count(*) FROM feedback WHERE user_id = $1`, []any{to}, 1},
		{`SELECT count(*) FROM user_sources WHERE user_id = $1`, []any{to}, 1},
		// from's press now means to's press; from's "cold" entries point to to's "cold".
		{`SELECT count(*) FROM session_exercises WHERE user_id = $1 AND exercise_id = $2`, []any{to, exFromPress}, 0},
		{`SELECT count(*) FROM journal_entries WHERE user_id = $1 AND behavior_id <> $2`, []any{to, bTo}, 0},
		// to's session of the shared id keeps exactly its own exercise.
		{`SELECT count(*) FROM session_exercises WHERE user_id = $1 AND session_id = $2`, []any{to, sShared}, 1},
		// the imported session keeps its origin, its source and its assisted set.
		{`SELECT count(*) FROM sessions WHERE user_id = $1 AND id = $2 AND origin = 'import' AND import_source = 'alpha_progression'`, []any{to, sOwn}, 1},
		{`SELECT count(*) FROM session_sets WHERE user_id = $1 AND assist_kg = 25`, []any{to}, 1},
	} {
		if got := count(c.q, c.args...); got != c.want {
			t.Errorf("%s: %d, want %d", c.q, got, c.want)
		}
	}
	// to's own session keeps exactly its one exercise (from's same-id session is dropped).
	if n := count(`SELECT count(*) FROM session_exercises WHERE user_id = $1 AND exercise_id = $2`, to, exToPress); n != 2 {
		t.Errorf("to's press: in to's session and in the moved one (remapped): %d", n)
	}
	if err := st.DeleteUser(ctx, from); err != nil {
		t.Fatalf("from is deleted afterwards: %v", err)
	}
	if n := owned(t, pool, from); n != 0 {
		t.Fatalf("from leaves nothing: %d", n)
	}
	if _, err := st.MergeUsers(ctx, from, to, false); !errors.Is(err, ErrFromNotFound) {
		t.Fatalf("a second merge: %v", err)
	}
}
