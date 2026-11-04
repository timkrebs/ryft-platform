package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
	"github.com/timkrebs/ryft/internal/testsupport"
)

// Tests of the same-source workout fold (docs/API.md -> Duplicate workouts; design D-c):
// on ingest inside the apply transaction, and the one-off `ingest fold-workouts`
// subcommand. Everything that writes runs through a pool opened AS ryft_ingest (and
// ryft_scoring for the re-score), never the superuser, because a statement that works
// as superuser failed as a service role in PR 1 and the tests did not catch it.
// Postgres-backed: they skip without RYFT_TEST_POSTGRES_URL like the rest of the package.

// ---- helpers --------------------------------------------------------------------

// foldRolePool opens a pool as role on admin's database. The password is the same string
// in every package (SetRolePassword is per server, tests of other packages run at once).
func foldRolePool(t *testing.T, admin *pgxpool.Pool, role string) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	pw := testsupport.RolePassword(role)
	if err := testsupport.WithRoleLock(ctx, func() error { return db.SetRolePassword(ctx, admin, role, pw) }); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(admin.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, pw)
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, u.String()
}

// foldEnv is a delEnv whose sync handlers use ryft_ingest's pool; e.pool and e.st stay the
// superuser's, for setup and for looking at the rows.
type foldEnv struct {
	*delEnv
	ingest    *pgxpool.Pool
	ingestURL string
	bin       string // the built ingest binary (lazily)
}

func newFoldEnv(t *testing.T) *foldEnv {
	t.Helper()
	pool := testPool(t)
	st := store.New(pool)
	ip, iurl := foldRolePool(t, pool, "ryft_ingest")
	e := &delEnv{t: t, pool: pool, st: st}
	e.user = e.newUser()
	f := &foldEnv{delEnv: e, ingest: ip, ingestURL: iurl}
	e.srv, e.buf = syncServer(t, store.New(ip), ip, e.user)
	return f
}

// with is f for the (sub)test t.
func (f *foldEnv) with(t *testing.T) *foldEnv {
	d := *f.delEnv
	d.t = t
	g := *f
	g.delEnv = &d
	return &g
}

// as is a sync handler (ryft_ingest) serving u.
func (f *foldEnv) as(u model.User) http.Handler {
	srv, _ := syncServer(f.t, store.New(f.ingest), f.ingest, u)
	return srv
}

// fresh is a new account (tz UTC in the database as well) and its handler.
func (f *foldEnv) fresh() (model.User, http.Handler) {
	f.t.Helper()
	u := f.newUser()
	f.exec(`UPDATE users SET tz = 'UTC' WHERE id = $1`, u.ID)
	return u, f.as(u)
}

// wk is a workout record.
func wk(id uuid.UUID, activity, source string, start, end time.Time) map[string]any {
	return map[string]any{"id": id.String(), "activity": activity, "start": start, "end": end, "source": source}
}

// put syncs ws through h and wants a 200.
func (f *foldEnv) put(h http.Handler, ws ...map[string]any) delOut {
	f.t.Helper()
	rec, out := send(h, part{workouts: ws}.body())
	if rec.Code != http.StatusOK {
		f.t.Fatalf("%d %s, want 200", rec.Code, rec.Body)
	}
	return out
}

// stored is the ids of the user's workouts, sorted.
func (f *foldEnv) stored(u model.User) []uuid.UUID {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT id FROM workouts WHERE user_id = $1`, u.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, id)
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return out
}

func sortedIDs(ids ...uuid.UUID) []uuid.UUID {
	out := slices.Clone(ids)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return out
}

// higher is the id Postgres orders last (the tie-break: the higher id is kept).
func higher(ids ...uuid.UUID) uuid.UUID { s := sortedIDs(ids...); return s[len(s)-1] }

func (f *foldEnv) wantStored(u model.User, what string, want ...uuid.UUID) {
	f.t.Helper()
	if got := f.stored(u); !slices.Equal(got, sortedIDs(want...)) {
		f.t.Errorf("%s: stored %v, want %v", what, got, sortedIDs(want...))
	}
}

func (f *foldEnv) pendingDays(u model.User) []string {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT day::text FROM score_queue WHERE user_id = $1 AND scored_at IS NULL ORDER BY day`, u.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		_ = rows.Scan(&d)
		out = append(out, d)
	}
	return out
}

// ---- same-source fold on ingest -------------------------------------------------

// The rule at its edges: starts and ends each within 120 s fold (the older upload goes,
// the newer stays), 121 s on either end does not; another source or another activity
// never folds. A copy of the same times from a different source stays.
func TestFoldSameSourceTable(t *testing.T) {
	f := newFoldEnv(t)
	s := func(n int) time.Duration { return time.Duration(n) * time.Second }
	for _, c := range []struct {
		name       string
		dStart     time.Duration
		dEnd       time.Duration
		activity2  string // of the second copy
		source2    string
		wantFolded bool
	}{
		{"identical times", 0, 0, "running", "Garmin Connect", true},
		{"120 s on both ends, later", s(120), s(120), "running", "Garmin Connect", true},
		{"120 s on both ends, earlier", s(-120), s(-120), "running", "Garmin Connect", true},
		{"120 s apart in opposite directions", s(120), s(-120), "running", "Garmin Connect", true},
		{"121 s on the start", s(121), 0, "running", "Garmin Connect", false},
		{"121 s before on the start", s(-121), 0, "running", "Garmin Connect", false},
		{"121 s on the end", 0, s(121), "running", "Garmin Connect", false},
		{"121 s before on the end", 0, s(-121), "running", "Garmin Connect", false},
		{"121 s on both", s(121), s(121), "running", "Garmin Connect", false},
		{"120 s start, 121 s end", s(120), s(121), "running", "Garmin Connect", false},
		{"other source", 0, 0, "running", "Apple Watch", false},
		{"other activity", 0, 0, "cycling", "Garmin Connect", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := f.with(t)
			u, h := g.fresh()
			at := base()
			older, newer := uuid.New(), uuid.New()
			g.put(h, wk(older, "running", "Garmin Connect", at, at.Add(time.Hour)))
			out := g.put(h, wk(newer, c.activity2, c.source2, at.Add(c.dStart), at.Add(time.Hour+c.dEnd)))
			if c.wantFolded {
				g.wantStored(u, "the older upload is folded into the newer", newer)
			} else {
				g.wantStored(u, "no fold", older, newer)
			}
			if out.Deleted.Workouts != 0 {
				t.Errorf("a fold is not a requested deletion: deleted %+v", out.Deleted)
			}
		})
	}
}

// "Most recently uploaded" is created_at, not start time: of three copies uploaded in
// the order X, Y, Z (starts not in that order) Z stays. A copy Health kept and the
// phone sends again later is the newest then and the other goes (convergence).
func TestFoldKeepsTheMostRecentlyUploaded(t *testing.T) {
	f := newFoldEnv(t)
	u, h := f.fresh()
	at := base()
	x, y, z := uuid.New(), uuid.New(), uuid.New()
	f.put(h, wk(x, "running", "Garmin Connect", at.Add(30*time.Second), at.Add(time.Hour)))
	f.put(h, wk(y, "running", "Garmin Connect", at.Add(60*time.Second), at.Add(time.Hour+10*time.Second)))
	f.put(h, wk(z, "running", "Garmin Connect", at, at.Add(time.Hour+30*time.Second)))
	f.wantStored(u, "the last upload stays, though it starts first", z)

	f.put(h, wk(x, "running", "Garmin Connect", at.Add(30*time.Second), at.Add(time.Hour)))
	f.wantStored(u, "a copy sent again is the newest upload", x)
}

// Copies of one request share one created_at; the higher id stays, whatever their order
// in the request.
func TestFoldTieKeepsTheHigherID(t *testing.T) {
	f := newFoldEnv(t)
	at := base()
	for _, reversed := range []bool{false, true} {
		u, h := f.fresh()
		a, b := uuid.New(), uuid.New()
		ws := []map[string]any{
			wk(a, "running", "Garmin Connect", at, at.Add(time.Hour)),
			wk(b, "running", "Garmin Connect", at.Add(40*time.Second), at.Add(time.Hour+20*time.Second)),
		}
		if reversed {
			ws[0], ws[1] = ws[1], ws[0]
		}
		out := f.put(h, ws...)
		f.wantStored(u, fmt.Sprintf("one request, reversed=%v: the higher id", reversed), higher(a, b))
		if out.Accepted.Workouts != 2 || out.Deleted.Workouts != 0 {
			t.Errorf("both copies were accepted, none deleted on request: accepted %+v, deleted %+v", out.Accepted, out.Deleted)
		}
	}
}

// One run, seven UUIDs (the Garmin pattern of 2026-10-20): re-written seven times in
// seven syncs, one row remains after each, the last upload; sent in one request, the
// highest id remains; a chain whose ends are further apart than 120 s but whose
// neighbours are not is one cluster too.
func TestFoldGarminSevenCopies(t *testing.T) {
	f := newFoldEnv(t)
	at := base()
	copyOf := func(id uuid.UUID, k int) map[string]any {
		return wk(id, "running", "Garmin Connect", at.Add(time.Duration(k*9)*time.Second), at.Add(time.Hour-time.Duration(k*8)*time.Second))
	}

	u, h := f.fresh()
	var last uuid.UUID
	for k := 0; k < 7; k++ {
		last = uuid.New()
		f.put(h, copyOf(last, k))
		if n := len(f.stored(u)); n != 1 {
			t.Fatalf("after copy %d: %d rows, want 1", k+1, n)
		}
	}
	f.wantStored(u, "seven syncs", last)

	u, h = f.fresh()
	ids := make([]uuid.UUID, 7)
	ws := make([]map[string]any, 7)
	for k := range ids {
		ids[k] = uuid.New()
		ws[k] = copyOf(ids[k], k)
	}
	out := f.put(h, ws...)
	f.wantStored(u, "one request with seven copies", higher(ids...))
	if out.Accepted.Workouts != 7 || out.Deleted.Workouts != 0 {
		t.Errorf("accepted %+v, deleted %+v", out.Accepted, out.Deleted)
	}

	u, h = f.fresh()
	chain := make([]uuid.UUID, 4)
	ws = ws[:0]
	for k := range chain {
		chain[k] = uuid.New()
		off := time.Duration(k*100) * time.Second // 0, 100, 200, 300 s: neighbours within 120 s, the ends not
		ws = append(ws, wk(chain[k], "running", "Garmin Connect", at.Add(off), at.Add(time.Hour+off)))
	}
	f.put(h, ws...)
	f.wantStored(u, "a chain is one cluster (connected components)", higher(chain...))
}

// A fold is not a requested deletion: deleted stays 0, but the days of the removed rows
// are queued and announced (here the removed copy is on the day before the kept one) in
// one event. The removed id then answers unknown_id to a deletion and is no workout.
func TestFoldQueuesDaysAndIsNotCountedAsDeleted(t *testing.T) {
	f := newFoldEnv(t)
	u, h := f.fresh()
	d := base()
	d = time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 0, 0, time.UTC) // 23:59 on D
	older, newer := uuid.New(), uuid.New()
	f.put(h, wk(older, "running", "Garmin Connect", d, d.Add(time.Hour)))
	f.exec(`DELETE FROM score_queue WHERE user_id = $1`, u.ID)
	f.exec(`DELETE FROM outbox WHERE user_id = $1`, u.ID)

	out := f.put(h, wk(newer, "running", "Garmin Connect", d.Add(90*time.Second), d.Add(time.Hour+90*time.Second))) // 00:00:30 on D+1
	f.wantStored(u, "folded", newer)
	wantCounts(t, "deleted", out.Deleted, delCounts{})
	wantCounts(t, "accepted", out.Accepted, delCounts{Workouts: 1})
	want := []string{day(d), day(d.AddDate(0, 0, 1)), day(d.AddDate(0, 0, 2))} // the removed row's D and D+1, the kept row's D+1 and D+2
	if strings.Join(out.Days, ",") != strings.Join(want, ",") || strings.Join(out.Scoring.QueuedDays, ",") != strings.Join(want, ",") {
		t.Errorf("days_touched %v, queued_days %v, want %v", out.Days, out.Scoring.QueuedDays, want)
	}
	if got := f.pendingDays(u); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("score_queue %v, want %v pending", got, want)
	}
	if n := f.count(`SELECT count(*) FROM outbox WHERE user_id = $1 AND publisher = 'ingest' AND routing_key = 'ingested' AND published_at IS NULL`, u.ID); n != 1 {
		t.Errorf("%d outbox events, want 1", n)
	}

	rec, del := send(h, part{deleted: []map[string]any{{"kind": "workout", "id": older.String()}, {"kind": "workout", "id": newer.String()}}}.body())
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	wantCounts(t, "deleting the folded id and the kept id: deleted", del.Deleted, delCounts{Workouts: 1})
	wantCounts(t, "deleting the folded id: unknown_id", del.unknownIDs(), delCounts{Workouts: 1})
	if _, err := f.st.Workout(context.Background(), u.ID, older); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the folded id is no workout (GET /v1/workouts/{id} answers 404): %v", err)
	}
}

// A workout of RYFT's own (source ryft-session) is never folded, whether a sync carries
// its source, carries another one, or the copies are older; the HealthKit copies next
// to it fold as usual.
func TestFoldNeverFoldsRyftSessionWorkouts(t *testing.T) {
	f := newFoldEnv(t)
	u, h := f.fresh()
	at := base()
	s1, s2, hk1, hk2 := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for k, id := range []uuid.UUID{s1, s2} {
		f.exec(`INSERT INTO workouts (id, user_id, activity, start_at, end_at, source) VALUES ($1, $2, 'strength', $3, $4, 'ryft-session')`,
			id, u.ID, at.Add(time.Duration(k*30)*time.Second), at.Add(time.Hour))
	}
	f.put(h, wk(hk1, "strength", "Apple Watch", at, at.Add(time.Hour)))
	f.put(h, wk(hk2, "strength", "Apple Watch", at.Add(time.Minute), at.Add(time.Hour+time.Minute)))
	f.wantStored(u, "the session workouts stay, the HealthKit copies fold", s1, s2, hk2)

	// A phone claiming the source: the rows are not touched either way.
	sent := uuid.New()
	f.put(h, wk(sent, "strength", "ryft-session", at.Add(10*time.Second), at.Add(time.Hour)))
	for _, id := range []uuid.UUID{s1, s2} {
		if f.hasWorkout(u, id) != 1 {
			t.Errorf("ryft-session workout %s must stay", id)
		}
	}
}

// What a sync folds: the request's workout sources in [min start - 1 day, max start + 1 day].
// Copies stored earlier next to the request fold with it (legacy rows); the same source far
// away, and another source, wait for the one-off subcommand. Another account is never touched.
func TestFoldSyncScope(t *testing.T) {
	f := newFoldEnv(t)
	u, h := f.fresh()
	other, _ := f.fresh()
	at := base()
	pair := func(user model.User, source string, start time.Time) (uuid.UUID, uuid.UUID) {
		a, b := uuid.New(), uuid.New()
		for k, id := range []uuid.UUID{a, b} {
			f.exec(`INSERT INTO workouts (id, user_id, activity, start_at, end_at, source, created_at) VALUES ($1, $2, 'running', $3, $4, $5, now() - interval '1 hour' + $6 * interval '1 second')`,
				id, user.ID, start.Add(time.Duration(k*10)*time.Second), start.Add(time.Hour), source, k)
		}
		return a, b
	}
	nearA, nearB := pair(u, "Garmin Connect", at.Add(-6*time.Hour))
	farA, farB := pair(u, "Garmin Connect", at.AddDate(0, 0, -10))
	wA, wB := pair(u, "Apple Watch", at.Add(-6*time.Hour))
	oA, oB := pair(other, "Garmin Connect", at.Add(-6*time.Hour))

	f.put(h, wk(uuid.New(), "cycling", "Garmin Connect", at, at.Add(time.Hour)))
	for _, id := range []uuid.UUID{nearA, nearB} {
		if (id == nearA) == (f.hasWorkout(u, id) == 1) { // the older upload (nearA) goes
			t.Errorf("legacy pair next to the request: %s present=%v, want only the newer one kept", id, f.hasWorkout(u, id))
		}
	}
	for _, id := range []uuid.UUID{farA, farB} {
		if f.hasWorkout(u, id) != 1 {
			t.Errorf("a pair 10 days away is out of the sync's scope (left to the subcommand): %s", id)
		}
	}
	for _, id := range []uuid.UUID{wA, wB} {
		if f.hasWorkout(u, id) != 1 {
			t.Errorf("another source than the request's is out of scope: %s", id)
		}
	}
	for _, id := range []uuid.UUID{oA, oB} {
		if f.hasWorkout(other, id) != 1 {
			t.Errorf("another account's rows are never touched: %s", id)
		}
	}
}

// Two syncs of one user at once, each inserting a copy of one run: whatever the
// interleaving, exactly one row remains and both answer 200 (the apply lock serialises
// the two transactions; the last one sees every committed copy).
func TestFoldConcurrentSyncsConverge(t *testing.T) {
	f := newFoldEnv(t)
	at := base()
	for round := 0; round < 12; round++ {
		u, _ := f.fresh()
		s1, s2 := f.as(u), f.as(u) // built before the goroutines: syncServer sets the default logger
		a, b := uuid.New(), uuid.New()
		bodies := []string{
			part{workouts: []map[string]any{wk(a, "running", "Garmin Connect", at, at.Add(time.Hour))}}.body(),
			part{workouts: []map[string]any{wk(b, "running", "Garmin Connect", at.Add(45*time.Second), at.Add(time.Hour+45*time.Second))}}.body(),
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		codes := make([]int, 2)
		for i, srv := range []http.Handler{s1, s2} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				rec, _ := send(srv, bodies[i])
				codes[i] = rec.Code
			}()
		}
		close(start)
		wg.Wait()
		if codes[0] != 200 || codes[1] != 200 {
			t.Fatalf("round %d: status %v, want 200 and 200", round, codes)
		}
		if got := f.stored(u); len(got) != 1 || (got[0] != a && got[0] != b) {
			t.Fatalf("round %d: stored %v, want exactly one of the two copies", round, got)
		}
	}
}

// A Train session attached to the folded copy stays (the row is kept) and is re-attached to
// the kept workout when the day is re-scored, by ryft_scoring: the fold moves no session_id.
func TestFoldedCopyWithASessionIsReattachedOnRescore(t *testing.T) {
	f := newFoldEnv(t)
	u, h := f.fresh()
	sp, _ := foldRolePool(t, f.pool, "ryft_scoring")
	engine := scoring.NewEngine(store.New(sp), slog.New(slog.NewTextHandler(io.Discard, nil)))
	engine.SetConsentMode(config.ModeOff)

	at := time.Date(base().Year(), base().Month(), base().Day(), 10, 0, 0, 0, time.UTC)
	older, newer, session := uuid.New(), uuid.New(), uuid.New()
	f.put(h, wk(older, "strength", "Apple Watch", at, at.Add(time.Hour)))
	f.exec(`INSERT INTO sessions (user_id, id, started_at, ended_at, status, workout_id) VALUES ($1, $2, $3, $4, 'completed', $5)`,
		u.ID, session, at, at.Add(time.Hour), older)
	f.exec(`UPDATE workouts SET session_id = $1, muscular_load = 40 WHERE id = $2`, session, older)

	f.put(h, wk(newer, "strength", "Apple Watch", at.Add(30*time.Second), at.Add(time.Hour+30*time.Second)))
	f.wantStored(u, "the older copy is folded", newer)
	if f.count(`SELECT count(*) FROM sessions WHERE user_id = $1 AND id = $2`, u.ID, session) != 1 {
		t.Fatal("the session row is kept")
	}

	if err := engine.ComputeDay(context.Background(), u.ID, day(at)); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT count(*) FROM workouts WHERE user_id = $1 AND id = $2 AND session_id = $3 AND muscular_load IS NOT NULL`, u.ID, newer, session); n != 1 {
		t.Errorf("the kept workout carries the session after the re-score: %d", n)
	}
	if n := f.count(`SELECT count(*) FROM sessions WHERE user_id = $1 AND id = $2 AND workout_id = $3`, u.ID, session, newer); n != 1 {
		t.Errorf("sessions.workout_id points at the kept workout: %d", n)
	}
	if f.count(`SELECT count(*) FROM workouts WHERE user_id = $1`, u.ID) != 1 {
		t.Error("no extra workout (no ryft-session one) is created for the session")
	}
}

// ---- ingest fold-workouts --------------------------------------------------------

// binary builds cmd/ingest once per environment.
func (f *foldEnv) binary() string {
	f.t.Helper()
	if f.bin == "" {
		f.bin = f.t.TempDir() + "/ingest"
		if out, err := exec.Command("go", "build", "-o", f.bin, ".").CombinedOutput(); err != nil {
			f.t.Fatalf("go build: %v\n%s", err, out)
		}
	}
	return f.bin
}

// fold runs `ingest fold-workouts args...` with ryft_ingest's credentials (databaseURL
// overrides them: "-" removes DATABASE_URL) and returns the combined output and exit code.
func (f *foldEnv) fold(databaseURL string, args ...string) (string, int) {
	f.t.Helper()
	bin := f.binary()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append([]string{"fold-workouts"}, args...)...)
	cmd.Dir = f.t.TempDir()
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "DATABASE_URL=") && !strings.HasPrefix(kv, "RYFT_TEST_") && !strings.HasPrefix(kv, "CONSENT_ENFORCE=") && !strings.HasPrefix(kv, "BETA_GATE=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	switch databaseURL {
	case "":
		cmd.Env = append(cmd.Env, "DATABASE_URL="+f.ingestURL)
	case "-":
	default:
		cmd.Env = append(cmd.Env, "DATABASE_URL="+databaseURL)
	}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &ee):
		return string(out), ee.ExitCode()
	default:
		f.t.Fatalf("run: %v", err)
		return "", -1
	}
}

// legacy stores a workout the way an old upload did, with a chosen created_at.
func (f *foldEnv) legacy(u model.User, id uuid.UUID, activity, source string, start, end, created time.Time) {
	f.t.Helper()
	f.exec(`INSERT INTO workouts (id, user_id, activity, start_at, end_at, source, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id, u.ID, activity, start, end, source, created)
}

// oneOff is the data of the subcommand tests: a user with a 3-copy cluster (day d1), a 2-copy
// cluster with a session (day d2), a lone workout, ryft-session rows and a different-source copy;
// another user with a pair whose created_at is equal (higher id stays).
type oneOff struct {
	u1, u2                           model.User
	d1, d2                           time.Time
	a1, a2, a3                       uuid.UUID // d1: a3 is the newest upload
	b1, b2                           uuid.UUID // d2: b2 is the newest upload; b1 carries the session
	lone, garmin, sess1, sess2, ride uuid.UUID
	x, y                             uuid.UUID // u2: same created_at
	session                          uuid.UUID
}

func (f *foldEnv) seedOneOff() oneOff {
	f.t.Helper()
	var o oneOff
	o.u1, _ = f.fresh()
	o.u2, _ = f.fresh()
	for _, u := range []model.User{o.u1, o.u2} {
		f.grant(u)
	}
	n := base()
	o.d1 = time.Date(n.Year(), n.Month(), n.Day(), 12, 0, 0, 0, time.UTC).AddDate(0, 0, -3)
	o.d2 = o.d1.AddDate(0, 0, -4)
	t0 := time.Now().Add(-48 * time.Hour).UTC()
	mn := time.Minute
	h := time.Hour
	o.a1, o.a2, o.a3 = uuid.New(), uuid.New(), uuid.New()
	f.legacy(o.u1, o.a1, "running", "Garmin Connect", o.d1, o.d1.Add(h), t0)
	f.legacy(o.u1, o.a2, "running", "Garmin Connect", o.d1.Add(50*time.Second), o.d1.Add(h+20*time.Second), t0.Add(mn))
	f.legacy(o.u1, o.a3, "running", "Garmin Connect", o.d1.Add(100*time.Second), o.d1.Add(h-90*time.Second), t0.Add(2*mn))
	o.b1, o.b2, o.session = uuid.New(), uuid.New(), uuid.New()
	f.legacy(o.u1, o.b1, "strength", "Apple Watch", o.d2, o.d2.Add(h), t0)
	f.legacy(o.u1, o.b2, "strength", "Apple Watch", o.d2.Add(30*time.Second), o.d2.Add(h+30*time.Second), t0.Add(mn))
	f.exec(`INSERT INTO sessions (user_id, id, started_at, ended_at, status, workout_id) VALUES ($1, $2, $3, $4, 'completed', $5)`,
		o.u1.ID, o.session, o.d2, o.d2.Add(h), o.b1)
	f.exec(`UPDATE workouts SET session_id = $1, muscular_load = 40 WHERE id = $2`, o.session, o.b1)
	o.lone, o.garmin, o.ride = uuid.New(), uuid.New(), uuid.New()
	f.legacy(o.u1, o.lone, "running", "Garmin Connect", o.d1.Add(6*h), o.d1.Add(7*h), t0) // far from the cluster
	f.legacy(o.u1, o.garmin, "running", "Apple Watch", o.d1, o.d1.Add(h), t0)             // same times, other source
	f.legacy(o.u1, o.ride, "cycling", "Garmin Connect", o.d1, o.d1.Add(h), t0)            // same times, other activity
	o.sess1, o.sess2 = uuid.New(), uuid.New()
	f.legacy(o.u1, o.sess1, "strength", "ryft-session", o.d1.Add(8*h), o.d1.Add(9*h), t0)
	f.legacy(o.u1, o.sess2, "strength", "ryft-session", o.d1.Add(8*h+time.Second), o.d1.Add(9*h), t0.Add(mn))
	o.x, o.y = uuid.New(), uuid.New()
	f.legacy(o.u2, o.x, "running", "Garmin Connect", o.d1, o.d1.Add(h), t0)
	f.legacy(o.u2, o.y, "running", "Garmin Connect", o.d1.Add(time.Minute), o.d1.Add(h+time.Minute), t0)
	return o
}

func (o oneOff) u1Kept() []uuid.UUID {
	return []uuid.UUID{o.a3, o.b2, o.lone, o.garmin, o.ride, o.sess1, o.sess2}
}

// -dry-run lists user, day, kept id and folded ids and changes nothing (no row, no queued
// day, no event); -user restricts the listing to one account.
func TestFoldWorkoutsDryRunListsAndChangesNothing(t *testing.T) {
	f := newFoldEnv(t)
	o := f.seedOneOff()
	f.exec(`DELETE FROM score_queue`)
	f.exec(`DELETE FROM outbox`)
	before := f.count(`SELECT count(*) FROM workouts`)

	out, code := f.fold("", "-dry-run")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{o.u1.ID.String(), o.u2.ID.String(), day(o.d1), day(o.d2),
		o.a3.String(), o.a1.String(), o.a2.String(), o.b2.String(), o.b1.String(), higher(o.x, o.y).String()} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing lacks %s:\n%s", want, out)
		}
	}
	for _, not := range []uuid.UUID{o.lone, o.garmin, o.ride, o.sess1, o.sess2} {
		if strings.Contains(out, not.String()) {
			t.Errorf("%s is no part of any fold and must not be listed:\n%s", not, out)
		}
	}
	if got := f.count(`SELECT count(*) FROM workouts`); got != before {
		t.Errorf("a dry run folded: %d rows, was %d", got, before)
	}
	if n := f.count(`SELECT count(*) FROM score_queue`) + f.count(`SELECT count(*) FROM outbox`); n != 0 {
		t.Errorf("a dry run queues no day and writes no event: %d rows", n)
	}

	out, code = f.fold("", "-dry-run", "-user", o.u2.ID.String())
	if code != 0 {
		t.Fatalf("-user: exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, o.u2.ID.String()) || strings.Contains(out, o.u1.ID.String()) || strings.Contains(out, o.a3.String()) {
		t.Errorf("-user lists that account only:\n%s", out)
	}
}

// A real run folds to the newest upload (equal created_at: the higher id), queues the
// folded rows' days, writes one event per user, keeps the session row and the ryft-session
// rows; -user leaves other accounts alone; the next run folds nothing and writes nothing.
func TestFoldWorkoutsRealRunIsIdempotentAndScoped(t *testing.T) {
	f := newFoldEnv(t)
	o := f.seedOneOff()
	f.exec(`DELETE FROM score_queue`)
	f.exec(`DELETE FROM outbox`)

	out, code := f.fold("", "-user", o.u1.ID.String())
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	f.wantStored(o.u1, "u1 folded to the newest upload of each cluster", o.u1Kept()...)
	f.wantStored(o.u2, "u2 is out of -user", o.x, o.y)
	for _, d := range []time.Time{o.d1, o.d2} {
		if !slices.Contains(f.pendingDays(o.u1), day(d)) {
			t.Errorf("day %s queued for u1: %v", day(d), f.pendingDays(o.u1))
		}
	}
	if got := f.pendingDays(o.u2); len(got) != 0 {
		t.Errorf("u2 has no day queued: %v", got)
	}
	var payload string
	if n := f.count(`SELECT count(*) FROM outbox WHERE user_id = $1 AND publisher = 'ingest' AND routing_key = 'ingested' AND published_at IS NULL`, o.u1.ID); n != 1 {
		t.Fatalf("%d events for u1, want 1", n)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT payload::text FROM outbox WHERE user_id = $1`, o.u1.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	for _, d := range []time.Time{o.d1, o.d2} {
		if !strings.Contains(payload, day(d)) {
			t.Errorf("the event %s lacks day %s", payload, day(d))
		}
	}
	if f.count(`SELECT count(*) FROM outbox WHERE user_id = $1`, o.u2.ID) != 0 {
		t.Error("no event for u2")
	}
	if f.count(`SELECT count(*) FROM sessions WHERE user_id = $1 AND id = $2`, o.u1.ID, o.session) != 1 {
		t.Error("the session row is kept (scoring re-attaches it)")
	}

	// All accounts now: only u2 has anything left.
	f.exec(`DELETE FROM score_queue`)
	f.exec(`DELETE FROM outbox`)
	if out, code = f.fold("", "-all"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	f.wantStored(o.u2, "equal created_at: the higher id stays", higher(o.x, o.y))
	f.wantStored(o.u1, "u1 unchanged", o.u1Kept()...)

	// And again: nothing to fold, nothing queued, nothing written.
	f.exec(`DELETE FROM score_queue`)
	f.exec(`DELETE FROM outbox`)
	rows := f.count(`SELECT count(*) FROM workouts`)
	if out, code = f.fold("", "-all"); code != 0 {
		t.Fatalf("second run: exit %d:\n%s", code, out)
	}
	if got := f.count(`SELECT count(*) FROM workouts`); got != rows {
		t.Errorf("the second run folded: %d rows, was %d", got, rows)
	}
	if n := f.count(`SELECT count(*) FROM score_queue`) + f.count(`SELECT count(*) FROM outbox`); n != 0 {
		t.Errorf("the second run queues and publishes nothing: %d rows", n)
	}
}

// Exit codes: 0 on success, 2 for a bad flag or a -user that is no
// UUID (nothing touched), 1 when the configuration is missing.
func TestFoldWorkoutsExitCodes(t *testing.T) {
	f := newFoldEnv(t)
	o := f.seedOneOff()
	rows := f.count(`SELECT count(*) FROM workouts`)

	for name, c := range map[string]struct {
		url  string
		args []string
		want int
	}{
		"unknown flag":        {"", []string{"-nonsense"}, 2},
		"user not a uuid":     {"", []string{"-user", "not-a-uuid"}, 2},
		"user without value":  {"", []string{"-user"}, 2},
		"no DATABASE_URL":     {"-", []string{"-dry-run"}, 1},
		"uuid as argument":    {"", []string{o.u1.ID.String(), "-dry-run"}, 2},
		"dry-run w/o dash":    {"", []string{"dry-run"}, 2},
		"-user then dry-run":  {"", []string{"-user", o.u1.ID.String(), "dry-run"}, 2},
		"-user then -all":     {"", []string{"-user", o.u1.ID.String(), "dry-run", "-all"}, 2},
		"everyone needs -all": {"", nil, 2},
	} {
		if out, code := f.fold(c.url, c.args...); code != c.want {
			t.Errorf("%s: exit %d, want %d:\n%s", name, code, c.want, out)
		}
	}
	if got := f.count(`SELECT count(*) FROM workouts`); got != rows {
		t.Errorf("none of these runs folds anything: %d rows, was %d", got, rows)
	}
}

// grant records consent to the current policy for u (the subcommand skips accounts without).
func (f *foldEnv) grant(u model.User) {
	f.t.Helper()
	if err := f.st.GrantConsent(context.Background(), u.ID, "2026-10-01", nil); err != nil {
		f.t.Fatal(err)
	}
}
