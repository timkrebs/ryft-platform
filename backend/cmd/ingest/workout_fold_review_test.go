package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
)

// Review round of PR 2: the subcommand's guards (arguments, -all, consent, cancelling), the
// caps, the overlap requirement and the lock order. Written against the spec; the constants
// the caps name are read from the source so this file compiles before they exist.

// srcConst reads `name = <int>` (or a regexp for the name) from the non-test Go files of dirs.
func srcConst(t *testing.T, nameRE string, dirs ...string) int {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*(?:const\s+)?(?:` + nameRE + `)\s*(?:int\s*)?=\s*([0-9_]+)`)
	for _, d := range dirs {
		files, _ := os.ReadDir(d)
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
				continue
			}
			raw, _ := os.ReadFile(d + "/" + f.Name())
			if m := re.FindSubmatch(raw); m != nil {
				n, err := strconv.Atoi(strings.ReplaceAll(string(m[1]), "_", ""))
				if err != nil {
					t.Fatal(err)
				}
				return n
			}
		}
	}
	t.Fatalf("no constant matching %q in %v yet", nameRE, dirs)
	return 0
}

// counter is the summed value of the counter family name whose labels include want.
func counter(t *testing.T, name string, want map[string]string) float64 {
	t.Helper()
	fams, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			have := map[string]string{}
			for _, l := range m.GetLabel() {
				have[l.GetName()] = l.GetValue()
			}
			ok := true
			for k, v := range want {
				ok = ok && have[k] == v
			}
			if ok {
				sum += m.GetCounter().GetValue()
			}
		}
	}
	return sum
}

// ---- 1. the subcommand's guards ---------------------------------------------------

// The destructive run over every account needs -all; without -user, -dry-run or -all it
// exits 2 and folds nothing. With -all it folds; a dry run over every account needs no -all.
func TestFoldWorkoutsAllIsRequiredForTheDestructiveRun(t *testing.T) {
	f := newFoldEnv(t)
	o := f.seedOneOff()
	before := f.count(`SELECT count(*) FROM workouts`)

	if out, code := f.fold(""); code != 2 {
		t.Fatalf("no -user, -dry-run or -all: exit %d, want 2:\n%s", code, out)
	}
	if got := f.count(`SELECT count(*) FROM workouts`); got != before {
		t.Fatalf("the refused run folded: %d rows, was %d", got, before)
	}
	if out, code := f.fold("", "-dry-run"); code != 0 || !strings.Contains(out, o.a3.String()) {
		t.Fatalf("dry-run over every account needs no -all: exit %d:\n%s", code, out)
	}
	if got := f.count(`SELECT count(*) FROM workouts`); got != before {
		t.Fatalf("dry run folded: %d rows, was %d", got, before)
	}
	if out, code := f.fold("", "-all"); code != 0 {
		t.Fatalf("-all: exit %d:\n%s", code, out)
	}
	f.wantStored(o.u1, "-all folds", o.u1Kept()...)
	f.wantStored(o.u2, "-all folds every account", higher(o.x, o.y))
}

// An account without consent is skipped, in a dry run and in a real run, with -user or
// without, its rows stay, and the summary counts the skip.
func TestFoldWorkoutsSkipsAccountsWithoutConsent(t *testing.T) {
	f := newFoldEnv(t)
	o := f.seedOneOff()
	nc, _ := f.fresh() // no consent
	at := time.Now().UTC().Truncate(time.Hour).AddDate(0, 0, -4)
	n1, n2 := uuid.New(), uuid.New()
	f.legacy(nc, n1, "running", "Garmin Connect", at, at.Add(time.Hour), time.Now().Add(-time.Hour))
	f.legacy(nc, n2, "running", "Garmin Connect", at.Add(time.Second), at.Add(time.Hour), time.Now().Add(-30*time.Minute))
	// Every account without consent counts: this one and the environment's own user.
	nSkip := f.count(`SELECT count(*) FROM users u WHERE NOT EXISTS (SELECT 1 FROM consents c WHERE c.user_id = u.id AND c.kind = 'health_processing' AND c.revoked_at IS NULL)`)
	skipped := regexp.MustCompile(fmt.Sprintf(`\b%d skipped\b`, nSkip))

	out, code := f.fold("", "-dry-run")
	if code != 0 {
		t.Fatalf("dry-run: exit %d:\n%s", code, out)
	}
	if strings.Contains(out, nc.ID.String()) || strings.Contains(out, n1.String()) {
		t.Errorf("dry run lists an account without consent:\n%s", out)
	}
	if !skipped.MatchString(out) {
		t.Errorf("the summary counts the skipped accounts:\n%s", out)
	}
	for name, args := range map[string][]string{"all": {"-all"}, "user": {"-user", nc.ID.String()}} {
		out, code = f.fold("", args...)
		if code != 0 {
			t.Fatalf("%s: exit %d:\n%s", name, code, out)
		}
		f.wantStored(nc, name+": the account without consent is untouched", n1, n2)
		if name == "all" {
			if !skipped.MatchString(out) {
				t.Errorf("the summary counts the skipped accounts:\n%s", out)
			}
			f.wantStored(o.u2, "all: the others fold", higher(o.x, o.y))
		}
	}
}

// SIGTERM while the subcommand waits on a user (here the apply lock, held by the test)
// ends it with exit 1, promptly, and leaves no user half folded: the blocked user is as it
// was. (What it cannot prove: that a signal between two users stops before the next one;
// that is the ctx.Err() check of the loop, which a lock held on every consenting user
// reaches at the first user.)
func TestFoldWorkoutsCancelStopsWithExit1AndFoldsNothingHalf(t *testing.T) {
	f := newFoldEnv(t)
	o := f.seedOneOff()
	before := f.count(`SELECT count(*) FROM workouts`)
	ctx := context.Background()

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, u := range []model.User{o.u1, o.u2} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7419005, hashtext($1::text))`, u.ID); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(f.binary(), "fold-workouts", "-all")
	cmd.Dir = t.TempDir()
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "DATABASE_URL=") && !strings.HasPrefix(kv, "RYFT_TEST_") && !strings.HasPrefix(kv, "CONSENT_ENFORCE=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "DATABASE_URL="+f.ingestURL)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	deadline := time.Now().Add(30 * time.Second)
	for f.count(`SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid WHERE l.locktype = 'advisory' AND NOT l.granted AND a.datname = current_database() AND a.usename = 'ryft_ingest'`) == 0 {
		select {
		case err := <-done:
			t.Fatalf("the subcommand ended (%v) instead of waiting on the user's apply lock:\n%s", err, out.String())
		default:
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("the subcommand never waited on the apply lock of a user:\n%s", out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var ee *exec.ExitError
		code := 0
		if err != nil {
			if !errors.As(err, &ee) {
				t.Fatal(err)
			}
			code = ee.ExitCode()
		}
		if code != 1 {
			t.Errorf("exit %d after SIGTERM, want 1:\n%s", code, out.String())
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("the subcommand ignored SIGTERM:\n%s", out.String())
	}
	_ = tx.Rollback(ctx)
	if got := f.count(`SELECT count(*) FROM workouts`); got != before {
		t.Errorf("a cancelled run left %d rows, was %d", got, before)
	}
}

// ---- 4. caps ----------------------------------------------------------------------

// More than model.MaxWorkouts workouts in one sync is a 422 that stores nothing (the
// samples of the request included).
func TestSyncWithTooManyWorkoutsIs422AndStoresNothing(t *testing.T) {
	f := newFoldEnv(t)
	limit := srcConst(t, `MaxWorkouts`, "../../internal/model")
	u, h := f.fresh()
	at := base()
	ws := make([]map[string]any, 0, limit+1)
	for i := 0; i <= limit; i++ {
		s := at.Add(time.Duration(i) * time.Hour)
		ws = append(ws, wk(uuid.New(), "running", "Watch", s, s.Add(time.Minute)))
	}
	rec, _ := send(h, part{workouts: ws, samples: []map[string]any{recSample("steps", at, 5, "W", nil)}}.body())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d workouts: %d, want 422", len(ws), rec.Code)
	}
	if n := f.count(`SELECT count(*) FROM workouts WHERE user_id = $1`, u.ID) + f.count(`SELECT count(*) FROM samples WHERE user_id = $1`, u.ID); n != 0 {
		t.Errorf("a 422 stores nothing: %d rows", n)
	}
	rec, _ = send(h, part{workouts: ws[:limit]}.body())
	if rec.Code != http.StatusOK {
		t.Errorf("exactly %d workouts: %d, want 200 (%s)", limit, rec.Code, rec.Body)
	}
}

// More rows in the fold's scope than the cap: the fold is skipped (every row kept, even a
// true copy), the sync answers 200, and ryft_workouts_fold_skipped_total{reason="too_many"} counts it.
func TestFoldSkippedAboveTheRowCap(t *testing.T) {
	f := newFoldEnv(t)
	limit := srcConst(t, `[A-Za-z]*[Ff]old[A-Za-z]*(?:Max|Cap|Limit|Rows)[A-Za-z]*|[Mm]ax[A-Za-z]*[Ff]old[A-Za-z]*`, "../../internal/store", ".")
	u, h := f.fresh()
	t0 := base().AddDate(0, 0, -20)
	// A true copy pair first, then limit+10 lone 45 s workouts a minute apart (no overlap).
	p1, p2 := uuid.New(), uuid.New()
	f.legacy(u, p1, "running", "Garmin Connect", t0, t0.Add(time.Hour), time.Now().Add(-2*time.Hour))
	f.legacy(u, p2, "running", "Garmin Connect", t0.Add(5*time.Second), t0.Add(time.Hour), time.Now().Add(-time.Hour))
	f.exec(`INSERT INTO workouts (id, user_id, activity, start_at, end_at, source)
		SELECT gen_random_uuid(), $1, 'running', $2::timestamptz + (n + 120) * interval '1 minute',
		       $2::timestamptz + (n + 120) * interval '1 minute' + interval '45 seconds', 'Garmin Connect'
		FROM generate_series(1, $3::int) n`, u.ID, t0, limit+10)
	before := f.count(`SELECT count(*) FROM workouts WHERE user_id = $1`, u.ID)
	skipped := func() float64 {
		return counter(t, "ryft_workouts_fold_skipped_total", map[string]string{"reason": "too_many"})
	}
	was := skipped()

	// The request spans the whole range, so the scope holds every row.
	end := t0.Add(time.Duration(limit+130) * time.Minute)
	rec, _ := send(h, part{workouts: []map[string]any{
		wk(uuid.New(), "cycling", "Garmin Connect", t0.Add(30*time.Minute), t0.Add(31*time.Minute)),
		wk(uuid.New(), "cycling", "Garmin Connect", end, end.Add(time.Minute)),
	}}.body())
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s, want 200", rec.Code, rec.Body)
	}
	if got := f.count(`SELECT count(*) FROM workouts WHERE user_id = $1`, u.ID); got != before+2 {
		t.Errorf("%d rows, want %d: the fold is skipped, nothing is removed", got, before+2)
	}
	if f.hasWorkout(u, p1) != 1 || f.hasWorkout(u, p2) != 1 {
		t.Error("the copy pair stays when the fold is skipped")
	}
	if d := skipped() - was; d != 1 {
		t.Errorf("ryft_workouts_fold_skipped_total{reason=too_many} grew by %v, want 1", d)
	}
}

// ---- 5. the overlap requirement of the same-source rule ---------------------------

// Six short workouts of one source, 45 s long and 60 s apart, never overlap: they are six
// workouts, through a sync (one request, and one by one) and through the subcommand.
// True copies (they overlap) still fold.
func TestFoldNeedsAnOverlap(t *testing.T) {
	f := newFoldEnv(t)
	at := base()
	six := func(source string) (ids []uuid.UUID, recs []map[string]any) {
		for i := 0; i < 6; i++ {
			id := uuid.New()
			s := at.Add(time.Duration(i) * time.Minute)
			ids = append(ids, id)
			recs = append(recs, wk(id, "other", source, s, s.Add(45*time.Second)))
		}
		return
	}

	u, h := f.fresh()
	ids, recs := six("Garmin Connect")
	f.put(h, recs...)
	f.wantStored(u, "six in one request", ids...)

	u, h = f.fresh()
	ids, recs = six("Garmin Connect")
	for _, r := range recs {
		f.put(h, r)
	}
	f.wantStored(u, "six one by one", ids...)

	// Through the subcommand: legacy rows plus a true copy pair that must fold.
	u, _ = f.fresh()
	f.grant(u)
	var legacy []uuid.UUID
	for i := 0; i < 6; i++ {
		id := uuid.New()
		legacy = append(legacy, id)
		s := at.Add(time.Duration(i) * time.Minute)
		f.legacy(u, id, "other", "Garmin Connect", s, s.Add(45*time.Second), time.Now().Add(-time.Duration(10-i)*time.Minute))
	}
	c1, c2 := uuid.New(), uuid.New()
	f.legacy(u, c1, "running", "Garmin Connect", at.Add(5*time.Hour), at.Add(6*time.Hour), time.Now().Add(-2*time.Hour))
	f.legacy(u, c2, "running", "Garmin Connect", at.Add(5*time.Hour+20*time.Second), at.Add(6*time.Hour), time.Now().Add(-time.Hour))
	if out, code := f.fold("", "-user", u.ID.String()); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	f.wantStored(u, "subcommand: the six stay, the true copies fold", append(legacy, c2)...)
}

// ---- 7. lock order ----------------------------------------------------------------

// SetWorkoutDuplicates (by a re-score, as ryft_scoring) at the same time as a sync whose
// fold removes a copy and a sync that only upserts, of one user, over several rounds: no
// request fails except with a retriable 503, nothing deadlocks (a deadlock is a 503 or an
// error naming 40P01, and both are reported), and after the retries the state converges:
// one copy, the cross-source duplicate marked, the lone workout there.
// What it cannot prove: the order of the locks themselves (a race needs the unlucky
// interleaving; 8 rounds make it likely, not certain) nor behaviour under real load.
func TestFoldRescoreAndUpsertLockOrder(t *testing.T) {
	f := newFoldEnv(t)
	sp, _ := foldRolePool(t, f.pool, "ryft_scoring")
	engine := scoring.NewEngine(store.New(sp), slog.New(slog.NewTextHandler(io.Discard, nil)))
	engine.SetConsentMode(config.ModeOff)
	d := base()
	d = time.Date(d.Year(), d.Month(), d.Day(), 10, 0, 0, 0, time.UTC)
	for round := 0; round < 8; round++ {
		u, _ := f.fresh()
		s1, s2 := f.as(u), f.as(u)
		watch, old, copyID, lone := uuid.New(), uuid.New(), uuid.New(), uuid.New()
		f.exec(`INSERT INTO workouts (id, user_id, activity, start_at, end_at, source) VALUES ($1,$2,'running',$3,$4,'Apple Watch')`, watch, u.ID, d, d.Add(time.Hour))
		f.exec(`INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source)
			SELECT $1, 'heart_rate', t, t, 140, 'bpm', 'Apple Watch' FROM generate_series($2::timestamptz, $2 + interval '59 minutes', interval '1 minute') t`, u.ID, d)
		f.exec(`INSERT INTO workouts (id, user_id, activity, start_at, end_at, source, created_at) VALUES ($1,$2,'running',$3,$4,'Garmin Connect', now() - interval '1 hour')`,
			old, u.ID, d.Add(2*time.Minute), d.Add(57*time.Minute))
		bodyFold := part{workouts: []map[string]any{wk(copyID, "running", "Garmin Connect", d.Add(2*time.Minute+30*time.Second), d.Add(57*time.Minute))}}.body()
		bodyLone := part{workouts: []map[string]any{wk(lone, "cycling", "Polar", d.Add(5*time.Hour), d.Add(6*time.Hour))}}.body()

		var wg sync.WaitGroup
		start := make(chan struct{})
		var mu sync.Mutex
		var problems []string
		note := func(format string, a ...any) {
			mu.Lock()
			problems = append(problems, fmt.Sprintf(format, a...))
			mu.Unlock()
		}
		codes := make([]int, 2)
		wg.Add(3)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 3; i++ {
				if err := engine.ComputeDay(context.Background(), u.ID, day(d)); err != nil {
					note("re-score %d: %v", i, err)
				}
			}
		}()
		for i, job := range []struct {
			srv  http.Handler
			body string
		}{{s1, bodyFold}, {s2, bodyLone}} {
			go func() {
				defer wg.Done()
				<-start
				rec, _ := send(job.srv, job.body)
				codes[i] = rec.Code
			}()
		}
		close(start)
		wg.Wait()
		for i, job := range []string{bodyFold, bodyLone} {
			for try := 0; codes[i] == http.StatusServiceUnavailable && try < 5; try++ {
				rec, _ := send([]http.Handler{s1, s2}[i], job)
				codes[i] = rec.Code
			}
			if codes[i] != http.StatusOK {
				note("sync %d answered %d (only a retriable 503 may fail, and the retries must succeed)", i, codes[i])
			}
		}
		for _, p := range problems {
			t.Errorf("round %d: %s", round, p)
		}
		if err := engine.ComputeDay(context.Background(), u.ID, day(d)); err != nil {
			t.Fatalf("round %d: final re-score: %v", round, err)
		}
		f.wantStored(u, fmt.Sprintf("round %d converged", round), watch, copyID, lone)
		if n := f.count(`SELECT count(*) FROM workouts WHERE user_id = $1 AND id = $2 AND duplicate_of = $3`, u.ID, copyID, watch); n != 1 {
			t.Errorf("round %d: the Garmin copy is marked as the Watch run's duplicate: %d", round, n)
		}
		if n := f.count(`SELECT count(*) FROM workouts WHERE user_id = $1 AND duplicate_of IS NOT NULL`, u.ID); n != 1 {
			t.Errorf("round %d: %d rows marked, want 1", round, n)
		}
		if t.Failed() {
			return
		}
	}
}
