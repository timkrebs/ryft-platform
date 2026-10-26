package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/train"
)

// testImportRules are stand-ins for train.SameSession, train.ImportConflict and train.SamePlan
// (the backend's pure rules): enough to drive the store. Equal means the same JSON after the
// server's fields are cleared and the times are normalised.
func testImportRules() ImportRules {
	norm := func(x model.Session) string {
		x.MuscularTRIMP, x.Strain, x.VolumeKG, x.WorkoutID, x.PRs, x.UpdatedAt, x.Origin = nil, nil, nil, nil, nil, time.Time{}, ""
		x.StartedAt = x.StartedAt.UTC().Truncate(time.Microsecond)
		if x.EndedAt != nil {
			x.EndedAt = ptr(x.EndedAt.UTC().Truncate(time.Microsecond))
		}
		x.Exercises = slices.Clone(x.Exercises)
		for i := range x.Exercises {
			x.Exercises[i].Sets = slices.Clone(x.Exercises[i].Sets)
			for j := range x.Exercises[i].Sets {
				s := &x.Exercises[i].Sets[j]
				s.UpdatedAt = s.UpdatedAt.UTC().Truncate(time.Microsecond)
				if s.CompletedAt != nil {
					s.CompletedAt = ptr(s.CompletedAt.UTC().Truncate(time.Microsecond))
				}
				if s.WeightKG == nil {
					s.WeightKG = ptr(0.0)
				}
				if s.AssistKG == nil {
					s.AssistKG = ptr(0.0)
				}
			}
		}
		b, _ := json.Marshal(x)
		return string(b)
	}
	return ImportRules{
		SameSession: func(stored, body model.Session) bool {
			body.Origin = ""
			return norm(stored) == norm(body)
		},
		Conflict: func(stored, body model.Session) bool {
			if stored.Status != model.SessionCompleted {
				return true
			}
			had := map[uuid.UUID]time.Time{}
			for _, e := range stored.Exercises {
				for _, s := range e.Sets {
					had[s.ID] = s.UpdatedAt
				}
			}
			for _, e := range body.Exercises {
				for _, s := range e.Sets {
					if at, ok := had[s.ID]; ok && at.After(s.UpdatedAt) {
						return true
					}
				}
			}
			return false
		},
		SamePlan: func(stored, body model.Plan) bool {
			clean := func(p model.Plan) string {
				p.Slug, p.IsBuiltin = nil, false
				for i := range p.Days {
					for j := range p.Days[i].Exercises {
						p.Days[i].Exercises[j].Exercise = nil
					}
				}
				b, _ := json.Marshal(p)
				return string(b)
			}
			return clean(stored) == clean(body)
		},
	}
}

var importWindow = DayWindow{From: "2025-09-19", To: "2026-10-26"}

func importStore(t *testing.T) (*Store, *pgxpool.Pool, uuid.UUID, uuid.UUID) {
	st, pool, a, b := trainStore(t)
	return st.WithImportRules(testImportRules()), pool, a, b
}

// imported is a finished session of an import: one exercise, two sets, completed.
func imported(ex uuid.UUID, start time.Time, kg ...float64) model.Session {
	end := start.Add(time.Hour)
	var sets []model.Set
	for i, w := range kg {
		sets = append(sets, lift(i, 5, w, start.Add(time.Duration(i)*time.Minute)))
	}
	x := newSession(ex, sets...)
	x.StartedAt, x.EndedAt, x.Status = start, &end, model.SessionCompleted
	return x
}

func importSess(t *testing.T, st *Store, user uuid.UUID, x model.Session) ImportOutcome {
	t.Helper()
	out, err := st.ImportSession(context.Background(), user, "alpha_progression", x, time.UTC, importWindow)
	if err != nil {
		t.Fatalf("ImportSession: %v", err)
	}
	return out
}

func count(t *testing.T, pool *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestImportSessionCreatedUnchangedUpdated(t *testing.T) {
	st, pool, a, _ := importStore(t)
	ctx := context.Background()
	x := imported(squat, t0, 100, 105)

	out := importSess(t, st, a, x)
	if out.Status != ImportCreated || out.Day != "2026-10-18" || !out.Queued || !slices.Equal(out.Exercises, []uuid.UUID{squat}) {
		t.Fatalf("create: %+v", out)
	}
	got, err := st.Session(ctx, a, x.ID)
	if err != nil || got.Origin != model.OriginImport || got.ImportSource == nil || *got.ImportSource != "alpha_progression" ||
		got.Status != model.SessionCompleted || got.MuscularTRIMP != nil || got.WorkoutID != nil || len(got.Exercises[0].Sets) != 2 {
		t.Fatalf("stored %+v err=%v", got, err)
	}
	if pending, _ := st.PendingDays(ctx, a); !slices.Equal(pending, []string{"2026-10-18"}) {
		t.Fatalf("the item queues its day in its own transaction: %v", pending)
	}

	// Unchanged writes nothing and queues nothing: same row version, same queue row.
	xminOf := func() string {
		var v string
		if err := pool.QueryRow(ctx, `SELECT s.xmin::text || '/' || (SELECT string_agg(st.xmin::text, ',' ORDER BY st.id) FROM session_sets st WHERE st.user_id = s.user_id)
			|| '/' || (SELECT queued_at::text FROM score_queue WHERE user_id = s.user_id) FROM sessions s WHERE s.user_id = $1 AND s.id = $2`, a, x.ID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := xminOf()
	time.Sleep(20 * time.Millisecond)
	if out := importSess(t, st, a, x); out.Status != ImportUnchanged || out.Queued || out.Day != "2026-10-18" {
		t.Fatalf("resend: %+v", out)
	}
	if after := xminOf(); after != before {
		t.Fatalf("unchanged must not touch a row: %s -> %s", before, after)
	}

	// Changed content replaces the item whole: the set the request leaves out is gone.
	y := x
	y.Exercises = []model.SessionExercise{x.Exercises[0]}
	y.Exercises[0].Sets = []model.Set{x.Exercises[0].Sets[0]}
	y.Exercises[0].Sets[0].Reps = ptr(8)
	if out := importSess(t, st, a, y); out.Status != ImportUpdated || !out.Queued || out.PreviousDay != "" {
		t.Fatalf("update: %+v", out)
	}
	got, _ = st.Session(ctx, a, x.ID)
	if len(got.Exercises[0].Sets) != 1 || *got.Exercises[0].Sets[0].Reps != 8 || got.Origin != model.OriginImport {
		t.Fatalf("replaced whole: %+v", got.Exercises)
	}
	if out := importSess(t, st, a, y); out.Status != ImportUnchanged {
		t.Fatalf("resend of the update: %+v", out)
	}

	// A different source is content (D126).
	if out, err := st.ImportSession(ctx, a, "other_app", y, time.UTC, importWindow); err != nil || out.Status != ImportUpdated {
		t.Fatalf("source changed: %+v %v", out, err)
	}
	if got, _ = st.Session(ctx, a, x.ID); *got.ImportSource != "other_app" {
		t.Fatalf("import_source %v", got.ImportSource)
	}
}

func TestImportSessionRejections(t *testing.T) {
	st, pool, a, b := importStore(t)
	ctx := context.Background()

	// An id logged in the app is not replaced.
	live := newSession(squat, lift(0, 5, 100, t0))
	if _, err := st.SaveSession(ctx, a, live); err != nil {
		t.Fatal(err)
	}
	if out := importSess(t, st, a, imported(squat, t0, 60)); out.Status != ImportCreated {
		t.Fatal("control")
	}
	appOwned := imported(squat, t0.Add(time.Hour), 50)
	appOwned.ID = live.ID
	if out := importSess(t, st, a, appOwned); out.Status != ImportRejected || out.Code != ImportIDTaken {
		t.Fatalf("app session id: %+v", out)
	}
	if got, _ := st.Session(ctx, a, live.ID); got.Origin != model.OriginApp || got.Status != model.SessionInProgress {
		t.Fatalf("the app's session is untouched: %+v", got)
	}
	// Another account's session id is taken as well: the row is its.
	other := imported(squat, t0, 70)
	importSess(t, st, b, other)
	steal := imported(squat, t0, 70)
	steal.ID = other.ID
	if out := importSess(t, st, a, steal); out.Status != ImportCreated {
		// ids are per user: (user_id, id). The same session id for another user is its own session.
		t.Fatalf("a session id is per user: %+v", out)
	}

	// Unknown references.
	if out := importSess(t, st, a, imported(uuid.New(), t0, 50)); out.Code != ImportUnknownExercise || out.Status != ImportRejected {
		t.Fatalf("unknown exercise: %+v", out)
	}
	withDay := imported(squat, t0, 50)
	withDay.PlanDayID = ptr(uuid.New())
	if out := importSess(t, st, a, withDay); out.Code != ImportUnknownPlanReference {
		t.Fatalf("unknown plan day: %+v", out)
	}
	withPE := imported(squat, t0, 50)
	withPE.Exercises[0].PlanExerciseID = ptr(uuid.New())
	if out := importSess(t, st, a, withPE); out.Code != ImportUnknownPlanReference {
		t.Fatalf("unknown plan exercise: %+v", out)
	}
	// A plan day of another user's plan is not visible either.
	p := userPlan()
	if out, err := st.ImportPlan(ctx, b, p); err != nil || out.Status != ImportCreated {
		t.Fatalf("plan for b: %+v %v", out, err)
	}
	foreign := imported(squat, t0, 50)
	foreign.PlanDayID = &p.Days[0].ID
	if out := importSess(t, st, a, foreign); out.Code != ImportUnknownPlanReference {
		t.Fatalf("another user's plan day: %+v", out)
	}

	// A set id that belongs to another of the user's sessions: id_taken, and nothing of the item stays.
	first := imported(squat, t0.Add(24*time.Hour), 80)
	importSess(t, st, a, first)
	clash := imported(bench, t0.Add(48*time.Hour), 40, 45)
	clash.Exercises[0].Sets[1].ID = first.Exercises[0].Sets[0].ID
	if out := importSess(t, st, a, clash); out.Status != ImportRejected || out.Code != ImportIDTaken {
		t.Fatalf("set id of another session: %+v", out)
	}
	clashEx := imported(bench, t0.Add(48*time.Hour), 40)
	clashEx.Exercises[0].ID = first.Exercises[0].ID
	if out := importSess(t, st, a, clashEx); out.Code != ImportIDTaken {
		t.Fatalf("session exercise id of another session: %+v", out)
	}
	for _, id := range []uuid.UUID{clash.ID, clashEx.ID} {
		if n := count(t, pool, `SELECT count(*) FROM sessions WHERE user_id=$1 AND id=$2`, a, id); n != 0 {
			t.Fatalf("a rejected item leaves no session (%d)", n)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM session_sets WHERE user_id=$1 AND session_exercise_id = $2`, a, clash.Exercises[0].ID); n != 0 {
		t.Fatalf("a rejected item leaves no sets (%d)", n)
	}
	if got, _ := st.Session(ctx, a, first.ID); len(got.Exercises[0].Sets) != 1 {
		t.Fatalf("the other session is untouched: %+v", got.Exercises)
	}

	// newer_on_server: the user changed a set after the import, or discarded the session.
	x := imported(squat, t0.Add(72*time.Hour), 90, 95)
	importSess(t, st, a, x)
	edited := x
	edited.Exercises = []model.SessionExercise{x.Exercises[0]}
	edited.Exercises[0].Sets = slices.Clone(x.Exercises[0].Sets)
	edited.Exercises[0].Sets[0].Reps = ptr(12)
	edited.Exercises[0].Sets[0].UpdatedAt = x.Exercises[0].Sets[0].UpdatedAt.Add(time.Hour)
	if res, err := st.SaveSession(ctx, a, edited); err != nil || res.Conflict {
		t.Fatalf("live edit: %+v %v", res, err)
	}
	if out := importSess(t, st, a, x); out.Status != ImportRejected || out.Code != ImportNewerOnServer {
		t.Fatalf("newer set: %+v", out)
	}
	if got, _ := st.Session(ctx, a, x.ID); *got.Exercises[0].Sets[0].Reps != 12 || got.Origin != model.OriginImport {
		t.Fatalf("the edit survives and stays imported: %+v", got.Exercises[0].Sets[0])
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET status='discarded' WHERE user_id=$1 AND id=$2`, a, first.ID); err != nil {
		t.Fatal(err)
	}
	if out := importSess(t, st, a, first); out.Code != ImportNewerOnServer {
		t.Fatalf("discarded: %+v", out)
	}

	// A live PUT never changes origin or import_source of an imported session (D125), whatever the body says.
	edited.Origin, edited.ImportSource = model.OriginApp, nil
	if _, err := st.SaveSession(ctx, a, edited); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Session(ctx, a, x.ID); got.Origin != model.OriginImport || got.ImportSource == nil || *got.ImportSource != "alpha_progression" {
		t.Fatalf("PUT must not touch origin: %+v", got)
	}
	// And a PUT with origin import creates an app session.
	liveNew := newSession(squat, lift(0, 5, 100, t0))
	liveNew.Origin, liveNew.ImportSource = model.OriginImport, ptr("x")
	if _, err := st.SaveSession(ctx, a, liveNew); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Session(ctx, a, liveNew.ID); got.Origin != model.OriginApp || got.ImportSource != nil {
		t.Fatalf("a PUT creates app sessions: %+v", got)
	}
}

func TestImportSessionWindowAndMove(t *testing.T) {
	st, _, a, _ := importStore(t)
	ctx := context.Background()
	// Older than the window: stored, not queued.
	old := imported(squat, time.Date(2024, 3, 1, 10, 0, 0, 0, time.UTC), 80)
	if out := importSess(t, st, a, old); out.Status != ImportCreated || out.Queued || out.Day != "2024-03-01" {
		t.Fatalf("old: %+v", out)
	}
	if pending, _ := st.PendingDays(ctx, a); len(pending) != 0 {
		t.Fatalf("nothing queued: %v", pending)
	}
	// An update that moves a session to another day queues both days.
	x := imported(squat, t0, 100)
	importSess(t, st, a, x)
	if _, err := st.pool.Exec(ctx, `UPDATE score_queue SET scored_at = now()`); err != nil {
		t.Fatal(err)
	}
	moved := x
	moved.StartedAt = t0.Add(-48 * time.Hour)
	end := moved.StartedAt.Add(time.Hour)
	moved.EndedAt = &end
	out := importSess(t, st, a, moved)
	if out.Status != ImportUpdated || out.Day != "2026-10-16" || out.PreviousDay != "2026-10-18" || !out.Queued {
		t.Fatalf("move: %+v", out)
	}
	if pending, _ := st.PendingDays(ctx, a); !slices.Equal(pending, []string{"2026-10-16", "2026-10-18"}) {
		t.Fatalf("both days pending: %v", pending)
	}
	// A move from outside into the window, and the window's local days (UTC+2 puts 23:30Z on the next day).
	berlin, _ := time.LoadLocation("Europe/Berlin")
	late := imported(squat, time.Date(2026, 10, 20, 23, 30, 0, 0, time.UTC), 70)
	if out, err := st.ImportSession(ctx, a, "alpha_progression", late, berlin, importWindow); err != nil || out.Day != "2026-10-21" {
		t.Fatalf("local day: %+v %v", out, err)
	}
}

func TestImportPlan(t *testing.T) {
	st, pool, a, b := importStore(t)
	ctx := context.Background()
	p := userPlan()
	if out, err := st.ImportPlan(ctx, a, p); err != nil || out.Status != ImportCreated {
		t.Fatalf("create: %+v %v", out, err)
	}
	var v1 string
	if err := pool.QueryRow(ctx, `SELECT xmin::text FROM plans WHERE id=$1`, p.ID).Scan(&v1); err != nil {
		t.Fatal(err)
	}
	if out, err := st.ImportPlan(ctx, a, p); err != nil || out.Status != ImportUnchanged {
		t.Fatalf("resend: %+v %v", out, err)
	}
	var v2 string
	_ = pool.QueryRow(ctx, `SELECT xmin::text FROM plans WHERE id=$1`, p.ID).Scan(&v2)
	if v1 != v2 {
		t.Fatalf("unchanged writes nothing: %s -> %s", v1, v2)
	}
	changed := p
	changed.Name = "Renamed"
	changed.Days = p.Days[:2]
	changed.DaysPerWeek = 2
	if out, err := st.ImportPlan(ctx, a, changed); err != nil || out.Status != ImportUpdated {
		t.Fatalf("update: %+v %v", out, err)
	}
	if got, _ := st.Plan(ctx, a, p.ID); got.Name != "Renamed" || len(got.Days) != 2 {
		t.Fatalf("replaced whole: %+v", got)
	}
	// Taken: another account's plan id, a built-in's, a day id of another of the user's plans.
	if out, _ := st.ImportPlan(ctx, b, p); out.Code != ImportIDTaken {
		t.Fatalf("another user's id: %+v", out)
	}
	builtins, _, _ := st.PlanSummaries(ctx, a)
	if len(builtins) == 0 {
		t.Skip("no built-in plans seeded")
	}
	bi := userPlan()
	bi.ID = builtins[0].ID
	if out, _ := st.ImportPlan(ctx, a, bi); out.Code != ImportIDTaken {
		t.Fatalf("built-in id: %+v", out)
	}
	share := userPlan()
	share.Days[0].ID = p.Days[0].ID
	if out, _ := st.ImportPlan(ctx, a, share); out.Code != ImportIDTaken {
		t.Fatalf("a day id of another plan: %+v", out)
	}
	if n := count(t, pool, `SELECT count(*) FROM plans WHERE id=$1`, share.ID); n != 0 {
		t.Fatal("a rejected plan leaves nothing")
	}
	unknown := userPlan()
	unknown.Days[0].Exercises[0].ExerciseID = uuid.New()
	if out, _ := st.ImportPlan(ctx, a, unknown); out.Code != ImportUnknownExercise {
		t.Fatalf("unknown exercise: %+v", out)
	}
	// A session of the same request can name the plan that was stored first.
	named := imported(squat, t0, 60)
	named.PlanDayID = &p.Days[0].ID
	named.Exercises[0].PlanExerciseID = &p.Days[0].Exercises[0].ID
	if out := importSess(t, st, a, named); out.Status != ImportCreated {
		t.Fatalf("session naming an imported plan: %+v", out)
	}
	// ... and it does not move the enrolment.
	if e, _ := st.ActiveEnrollment(ctx, a); e != nil {
		t.Fatalf("importing enrols nothing: %+v", e)
	}
}

func TestImportLease(t *testing.T) {
	st, pool, a, b := importStore(t)
	ctx := context.Background()
	rel, ok, err := st.AcquireImportLease(ctx, a, ImportLeaseTTL)
	if err != nil || !ok {
		t.Fatalf("first: ok=%v %v", ok, err)
	}
	if _, ok, err := st.AcquireImportLease(ctx, a, ImportLeaseTTL); err != nil || ok {
		t.Fatalf("second must be busy: ok=%v %v", ok, err)
	}
	if relB, ok, err := st.AcquireImportLease(ctx, b, ImportLeaseTTL); err != nil || !ok {
		t.Fatalf("another user is independent: ok=%v %v", ok, err)
	} else {
		relB(ctx)
	}
	// release works on a cancelled context.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	rel(cancelled)
	rel2, ok, err := st.AcquireImportLease(ctx, a, ImportLeaseTTL)
	if err != nil || !ok {
		t.Fatalf("after release: ok=%v %v", ok, err)
	}
	// Expiry: a lease that ran out can be taken, and the old holder's release does not free the new one.
	if _, err := pool.Exec(ctx, `UPDATE users SET import_lease_until = now() - interval '1 second' WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	rel3, ok, err := st.AcquireImportLease(ctx, a, ImportLeaseTTL)
	if err != nil || !ok {
		t.Fatalf("after expiry: ok=%v %v", ok, err)
	}
	rel2(ctx) // the first holder finishes late
	if _, ok, _ := st.AcquireImportLease(ctx, a, ImportLeaseTTL); ok {
		t.Fatal("a late release must not free a lease that another import took")
	}
	rel3(ctx)
	if n := count(t, pool, `SELECT count(*) FROM users WHERE id=$1 AND import_lease_until IS NULL`, a); n != 1 {
		t.Fatal("released")
	}
	if _, _, err := st.AcquireImportLease(ctx, uuid.New(), ImportLeaseTTL); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such account: %v", err)
	}
	// Many at once: exactly one wins.
	wins := make(chan bool, 8)
	for range 8 {
		go func() {
			_, ok, err := st.AcquireImportLease(ctx, b, ImportLeaseTTL)
			wins <- ok && err == nil
		}()
	}
	n := 0
	for range 8 {
		if <-wins {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d of 8 concurrent acquires won", n)
	}
}

func TestImportDays(t *testing.T) {
	st, _, a, _ := importStore(t)
	ctx := context.Background()
	d := func(days int) time.Time { return t0.Add(time.Duration(days) * 24 * time.Hour) }
	// History: squat 10 days ago, bench 8, squat 5, squat in 2024 (outside the window).
	s1, s2, s3 := imported(squat, d(-10), 100), imported(bench, d(-8), 60), imported(squat, d(-5), 105)
	old := imported(squat, time.Date(2024, 1, 5, 9, 0, 0, 0, time.UTC), 90)
	for _, x := range []model.Session{s1, s2, s3, old} {
		importSess(t, st, a, x)
	}
	// A squat session in progress does not count.
	prog := newSession(squat, lift(0, 5, 100, d(-3)))
	prog.StartedAt = d(-3)
	if _, err := st.SaveSession(ctx, a, prog); err != nil {
		t.Fatal(err)
	}
	got, err := st.DependentDays(ctx, a, d(-9), []uuid.UUID{squat}, importWindow, time.UTC)
	if err != nil || !slices.Equal(got, []string{"2026-10-13"}) {
		t.Fatalf("after day -9 with squat: %v %v", got, err)
	}
	got, _ = st.DependentDays(ctx, a, d(-20), []uuid.UUID{squat, bench}, importWindow, time.UTC)
	if !slices.Equal(got, []string{"2026-10-08", "2026-10-10", "2026-10-13"}) {
		t.Fatalf("both exercises: %v (the 2024 session is outside the window)", got)
	}
	got, _ = st.DependentDays(ctx, a, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), []uuid.UUID{squat}, importWindow, time.UTC)
	if slices.Contains(got, "2024-01-05") {
		t.Fatalf("the window cuts: %v", got)
	}
	if got, _ = st.DependentDays(ctx, a, d(-9), nil, importWindow, time.UTC); len(got) != 0 {
		t.Fatalf("no exercises: %v", got)
	}
	if got, _ = st.DependentDays(ctx, a, d(-5), []uuid.UUID{squat}, importWindow, time.UTC); len(got) != 0 {
		t.Fatalf("strictly after since: %v", got)
	}

	// PendingAmong: queued and unscored only.
	if _, err := st.pool.Exec(ctx, `UPDATE score_queue SET scored_at = now() WHERE user_id = $1 AND day = '2026-10-08'`, a); err != nil {
		t.Fatal(err)
	}
	pend, err := st.PendingAmong(ctx, a, []string{"2026-10-08", "2026-10-10", "2026-10-13", "2026-10-14", "2024-01-05"})
	if err != nil || !slices.Equal(pend, []string{"2026-10-10", "2026-10-13"}) {
		t.Fatalf("pending: %v %v", pend, err)
	}
	if pend, _ := st.PendingAmong(ctx, a, nil); pend == nil || len(pend) != 0 {
		t.Fatalf("empty, not nil: %#v", pend)
	}
}

func TestFinishImportIsAtomic(t *testing.T) {
	st, pool, a, _ := importStore(t)
	ctx := context.Background()
	ev, err := events.Ingested(model.IngestedEvent{UserID: a, Days: []string{"2026-10-17", "2026-10-18"}, Reason: "import", RequestID: "r1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishImport(ctx, a, nil, ev); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM outbox WHERE user_id=$1`, a); n != 0 {
		t.Fatal("no days, no event")
	}
	if err := st.FinishImport(ctx, a, []string{"2026-10-17", "2026-10-18"}, ev); err != nil {
		t.Fatal(err)
	}
	var key, reason, payload string
	var pub string
	if err := pool.QueryRow(ctx, `SELECT routing_key, reason, payload::text, publisher FROM outbox WHERE user_id=$1`, a).Scan(&key, &reason, &payload, &pub); err != nil {
		t.Fatalf("exactly one outbox row: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(payload), &body)
	if key != events.KeyIngested || reason != "import" || pub != "api" || body["reason"] != "import" || body["session_id"] != nil {
		t.Fatalf("event %s %s %s %v", key, reason, pub, body)
	}
	if pending, _ := st.PendingDays(ctx, a); !slices.Equal(pending, []string{"2026-10-17", "2026-10-18"}) {
		t.Fatalf("queued: %v", pending)
	}

	// Atomic: a failing outbox insert leaves no queue rows.
	if _, err := pool.Exec(ctx, `DELETE FROM score_queue WHERE user_id=$1`, a); err != nil {
		t.Fatal(err)
	}
	bad := ev
	bad.Reason = "bad\x00" // Postgres text cannot hold a NUL
	if err := st.FinishImport(ctx, a, []string{"2026-10-20"}, bad); err == nil {
		t.Fatal("a NUL in the reason cannot be stored")
	}
	if pending, _ := st.PendingDays(ctx, a); len(pending) != 0 {
		t.Fatalf("the failed event rolled the queue back: %v", pending)
	}
}

func TestImportRecordsAreSilentInCardsAndNoticesCarryOrigin(t *testing.T) {
	st, _, a, _ := importStore(t)
	ctx := context.Background()
	imp := imported(squat, t0, 100)
	importSess(t, st, a, imp)
	live := newSession(bench, lift(0, 5, 60, t0.Add(time.Hour)))
	if _, err := st.SaveSession(ctx, a, live); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		session, ex uuid.UUID
	}{{imp.ID, squat}, {live.ID, bench}} {
		set := imp.Exercises[0].Sets[0].ID
		if r.session == live.ID {
			set = live.Exercises[0].Sets[0].ID
		}
		if err := st.ReplacePRs(ctx, a, r.ex, []model.PR{{ExerciseID: r.ex, Kind: model.PRMaxWeight, Value: 100, SetID: set, SessionID: r.session, AchievedAt: t0}}); err != nil {
			t.Fatal(err)
		}
	}
	prs, err := st.PRsSince(ctx, a, t0.Add(-time.Hour))
	if err != nil || len(prs) != 1 || prs[0].SessionID != live.ID {
		t.Fatalf("the cards skip the imported record: %+v %v", prs, err)
	}
	if got, _ := st.ExercisePRs(ctx, a, squat); len(got) != 1 {
		t.Fatalf("the record itself is kept: %+v", got)
	}
	if got, _ := st.SessionPRs(ctx, a, imp.ID); len(got) != 1 {
		t.Fatalf("SessionPRs keeps it: %+v", got)
	}
	n, err := st.SessionNotice(ctx, a, imp.ID)
	if err != nil || n.Origin != model.OriginImport {
		t.Fatalf("notice origin: %+v %v", n, err)
	}
	if n, _ = st.SessionNotice(ctx, a, live.ID); n.Origin != model.OriginApp {
		t.Fatalf("notice origin: %+v", n)
	}
	// Summaries and the list carry the origin.
	list, err := st.SessionSummaries(ctx, a, t0.Add(-time.Hour), t0.Add(3*time.Hour), "", true)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, s := range list {
		src := ""
		if s.ImportSource != nil {
			src = *s.ImportSource
		}
		seen[s.Origin] = src
	}
	if seen[model.OriginImport] != "alpha_progression" || seen[model.OriginApp] != "" || len(seen) != 2 {
		t.Fatalf("summaries: %v", seen)
	}
}

func TestAssistKG(t *testing.T) {
	st, pool, a, _ := importStore(t)
	ctx := context.Background()
	pull := train2Bodyweight(t, st, a)
	x := imported(pull, t0, 0)
	x.Exercises[0].Sets[0].WeightKG = nil
	x.Exercises[0].Sets[0].AssistKG = ptr(20.0)
	if out := importSess(t, st, a, x); out.Status != ImportCreated {
		t.Fatalf("%+v", out)
	}
	got, _ := st.Session(ctx, a, x.ID)
	if s := got.Exercises[0].Sets[0]; s.AssistKG == nil || *s.AssistKG != 20 || s.WeightKG != nil {
		t.Fatalf("round trip: %+v", s)
	}
	// LoggedSets and the history read it too.
	ls, _ := st.LoggedSets(ctx, a, []uuid.UUID{pull}, t0.Add(24*time.Hour), uuid.Nil)
	if len(ls) != 1 || ls[0].AssistKG == nil || *ls[0].AssistKG != 20 {
		t.Fatalf("LoggedSets: %+v", ls)
	}
	// The same values, assist null versus 0, compare equal in the rules the store is given, and a change updates.
	x.Exercises[0].Sets[0].AssistKG = ptr(0.0)
	if out := importSess(t, st, a, x); out.Status != ImportUpdated {
		t.Fatalf("assist changed: %+v", out)
	}
	// The column refuses what the clamp should have caught.
	if _, err := pool.Exec(ctx, `UPDATE session_sets SET assist_kg = 201 WHERE user_id = $1`, a); err == nil {
		t.Fatal("CHECK 0-200")
	}
}

// train2Bodyweight is a custom bodyweight exercise (the built-ins are all seeded as weighted or not; a
// custom one keeps the test independent of the seed).
func train2Bodyweight(t *testing.T, st *Store, user uuid.UUID) uuid.UUID {
	t.Helper()
	e, err := train.NewExercise(model.ExerciseInput{ID: uuid.New(), Name: "Assisted pull-up", Kind: model.KindBodyweight,
		PrimaryMuscles: []string{"lats"}, LoadType: model.LoadBodyweight})
	if err != nil {
		t.Fatal(err)
	}
	e.LoadFactor = 1
	if _, _, err := st.SaveCustomExercise(context.Background(), user, e); err != nil {
		t.Fatal(err)
	}
	return e.ID
}

func TestImportItemsAreIndependentTransactions(t *testing.T) {
	st, pool, a, _ := importStore(t)
	ctx := context.Background()
	ok1, bad, ok2 := imported(squat, t0, 100), imported(uuid.New(), t0.Add(time.Hour), 50), imported(bench, t0.Add(2*time.Hour), 60)
	var got []string
	for _, x := range []model.Session{ok1, bad, ok2} {
		got = append(got, importSess(t, st, a, x).Status)
	}
	if !slices.Equal(got, []string{ImportCreated, ImportRejected, ImportCreated}) {
		t.Fatalf("partial success: %v", got)
	}
	if n := count(t, pool, `SELECT count(*) FROM sessions WHERE user_id=$1`, a); n != 2 {
		t.Fatalf("two stored: %d", n)
	}
	// A cancelled context is a failure, not a rejection, and stores nothing.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := st.ImportSession(cancelled, a, "alpha_progression", imported(squat, t0.Add(5*time.Hour), 70), time.UTC, importWindow); err == nil {
		t.Fatal("a database failure must be returned, not turned into a rejection")
	}
	// Without the rules the store refuses to guess.
	bare := New(st.pool)
	if _, err := bare.ImportSession(ctx, a, "alpha_progression", ok1, time.UTC, importWindow); !errors.Is(err, ErrImportRules) {
		t.Fatalf("rules not set: %v", err)
	}
}

// A request at the size limit: 50 sessions of 10 exercises and 5 sets (2 500 sets), then the same
// again, then every session changed. The time is logged (round trips dominate on a remote
// database: about a dozen statements per item); the bound only catches a plan that went wrong.
func TestImportWriteCost(t *testing.T) {
	st, pool, a, _ := importStore(t)
	ids := []uuid.UUID{squat, bench, train.BuiltinExerciseID("deadlift"), train.BuiltinExerciseID("overhead-press"),
		train.BuiltinExerciseID("barbell-row")}
	var batch []model.Session
	for n := range 50 {
		x := imported(ids[0], t0.Add(-time.Duration(n+1)*24*time.Hour), 100)
		x.Exercises = nil
		for k := range 10 {
			e := model.SessionExercise{ID: uuid.New(), ExerciseID: ids[k%len(ids)], Position: k}
			for i := range 5 {
				e.Sets = append(e.Sets, lift(i, 5, 60, x.StartedAt.Add(time.Duration(k*5+i)*time.Minute)))
			}
			x.Exercises = append(x.Exercises, e)
		}
		batch = append(batch, x)
	}
	run := func(name string, want string) {
		start := time.Now()
		for _, x := range batch {
			if out := importSess(t, st, a, x); out.Status != want {
				t.Fatalf("%s: %+v", name, out)
			}
		}
		took := time.Since(start)
		t.Logf("%s: 50 sessions, 2 500 sets: %s", name, took.Round(time.Millisecond))
		if took > 20*time.Second {
			t.Fatalf("%s took %s", name, took)
		}
	}
	run("create", ImportCreated)
	run("resend", ImportUnchanged)
	for i := range batch {
		batch[i].Exercises[0].Sets[0].Reps = ptr(9)
	}
	run("update", ImportUpdated)
	if n := count(t, pool, `SELECT count(*) FROM session_sets WHERE user_id=$1`, a); n != 2500 {
		t.Fatalf("sets %d", n)
	}
}

// Two imports and a live save of the same session id do not interleave: the advisory lock serialises them.
func TestImportSessionWaitsForALiveSave(t *testing.T) {
	st, pool, a, _ := importStore(t)
	ctx := context.Background()
	x := imported(squat, t0, 100)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, lockSession, a, x.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan ImportOutcome, 1)
	go func() {
		out, _ := st.ImportSession(ctx, a, "alpha_progression", x, time.UTC, importWindow)
		done <- out
	}()
	select {
	case <-done:
		t.Fatal("the import must wait for the save holding the session's lock")
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if out := <-done; out.Status != ImportCreated {
		t.Fatalf("after the lock: %+v", out)
	}
}
