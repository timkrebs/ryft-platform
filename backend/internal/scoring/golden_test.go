package scoring

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
	"github.com/timkrebs/ryft/internal/train"
)

// Golden files: the safety net for refactors (docs/SCORING.md → Model version,
// D149). The generator's users (golden_gen_test.go) go through the store calls
// ingest and the api make (a sync a day with its apply and fold, sessions saved
// and completed or imported) and the real Engine (ComputeRange, as
// scripts/rescore_all.sh does), then a second phase (records that arrive after
// the month was scored, and a re-score). Everything scoring writes must then
// equal testdata/golden/<user>.json: every daily_scores column, and the
// workouts (strain, trimp, zones, duplicate_of, the attached session and its
// load, the ones scoring creates for sessions) and sessions (load and workout)
// rows, ids replaced by readable labels. Nothing here is worked out by hand:
// the calibration tests pin what a number should be, this pins that it did not
// move. On the way it checks that scoring is idempotent (a second ComputeRange
// moves nothing), that newest-first gives the same days, and that no user's
// scoring wrote another's rows.
//
// A diff means a stored number moved: a bug, or a model change, which bumps
// ModelVersion (version.go, changelog.json, SCORING.md) and then regenerates
// with `make golden-update`. The guard (golden_diff_test.go) and
// scripts/golden_guard.sh keep a regeneration honest.
//
// Determinism: the generator's, plus the read in UTC with Postgres's shortest
// exact floats, and canonical JSON (sorted keys, numbers rounded to 1e-6 and
// printed shortest, which also absorbs last-bit differences between CPUs).
//
// Opt-in like every engine test (RYFT_TEST_POSTGRES_URL), but never skipped in
// CI. It runs on the test's superuser pool, so the ryft_scoring grants and RLS
// are not exercised here; HealthKit deletions are not part of the generator.

// goldenNewestFirst is how many of the last days are scored again newest first.
const goldenNewestFirst = 10

// goldenRun stores the user's month the way ingest and the api do, scores it
// in two phases and checks idempotence and order. It returns the user's file
// and how to read it again (with the same labels).
func goldenRun(t *testing.T, st *store.Store, pool *pgxpool.Pool, engine *Engine, u goldenUser, g *goldenGen) (goldenFile, func(*testing.T) goldenFile) {
	t.Helper()
	ctx := context.Background()
	user := uuid.NewSHA1(goldenNS, []byte(u.Name))
	if _, err := st.EnsureUser(ctx, store.Identity{ID: user}); err != nil {
		t.Fatal(err)
	}
	patch := model.UserPatch{TZ: &u.TZ}
	var estimate *int
	if u.MaxHR > 0 {
		patch.MaxHR = &u.MaxHR
	}
	if u.BirthYear > 0 { // as PATCH /v1/me: the estimate for the birth year (D81)
		e := EstimatedMaxHR(u.BirthYear, goldenNow.Year())
		patch.BirthYear, estimate = &u.BirthYear, &e
	}
	if _, err := st.UpdateUser(ctx, user, patch, estimate); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantConsent(ctx, user, "golden", nil); err != nil {
		t.Fatal(err)
	}

	// One sync per local day (by start), then the re-sent copies in one more.
	type sync struct {
		samples  []model.Sample
		sleep    []model.SleepSegment
		workouts []model.Workout
	}
	syncs := map[string]*sync{}
	of := func(at time.Time) *sync {
		d := at.In(g.loc).Format(dayLayout)
		if syncs[d] == nil {
			syncs[d] = &sync{}
		}
		return syncs[d]
	}
	for _, s := range g.samples {
		of(s.Start).samples = append(of(s.Start).samples, s)
	}
	for _, s := range g.sleep {
		of(s.Start).sleep = append(of(s.Start).sleep, s)
	}
	for _, w := range g.workouts {
		of(w.Start).workouts = append(of(w.Start).workouts, w)
	}
	order := slices.Sorted(maps.Keys(syncs))
	if len(g.resends) > 0 {
		syncs["resend"], order = &sync{workouts: g.resends}, append(order, "resend")
	}
	for _, d := range order {
		goldenSync(t, st, user, g.loc, syncs[d].samples, syncs[d].sleep, syncs[d].workouts)
	}

	// Sessions: the app's saved and completed, then one import request.
	exercises := map[uuid.UUID]model.Exercise{}
	for _, e := range g.lib {
		exercises[e.ID] = e
	}
	var imports []model.Session
	for _, gs := range g.sessions {
		s := gs.Session
		if gs.Imported {
			imports = append(imports, s)
			continue
		}
		end := *s.EndedAt
		s.Status = model.SessionInProgress
		if err := train.CheckSession(&s, goldenNow); err != nil {
			t.Fatalf("session %s: %v", s.Title, err)
		}
		if _, err := st.SaveSession(ctx, user, s); err != nil {
			t.Fatalf("session %s: %v", s.Title, err)
		}
		if err := st.CompleteSession(ctx, user, s.ID, store.Completion{EndedAt: end}); err != nil {
			t.Fatalf("complete %s: %v", s.Title, err)
		}
	}
	from, to := train.ImportWindow(goldenNow, g.loc)
	for _, k := range train.ImportOrder(imports) { // oldest first, as cmd/api/train_import.go
		s := imports[k]
		s.Status = model.SessionCompleted
		if err := train.CheckImportSession(&s, goldenNow, exercises); err != nil {
			t.Fatalf("import %s: %v", s.Title, err)
		}
		out, err := st.ImportSession(ctx, user, model.ImportSourceAlphaProgression, s, g.loc, store.DayWindow{From: from, To: to})
		if err != nil || out.Status != store.ImportCreated {
			t.Fatalf("import %s: %+v %v", s.Title, out, err)
		}
	}

	labels := map[string]string{}
	label := func(id uuid.UUID, l string) {
		if old, ok := labels[id.String()]; ok && old != l {
			t.Fatalf("golden: %s and %s share an id", old, l)
		}
		labels[id.String()] = l
	}
	name := func(w model.Workout) string {
		return "workout " + w.Source + " " + w.Start.In(g.loc).Format("2006-01-02T15:04")
	}
	for _, w := range g.workouts {
		label(w.ID, name(w))
	}
	for _, w := range g.resends {
		label(w.ID, name(w)+" resent")
	}
	for _, w := range g.late {
		label(w.ID, name(w)+" late")
	}
	for _, gs := range g.sessions {
		label(gs.Session.ID, "session "+gs.Session.Title)
		label(train.SessionWorkoutID(user, gs.Session.ID), "workout created for session "+gs.Session.Title)
	}
	read := func(t *testing.T) goldenFile {
		t.Helper()
		f := goldenFile{User: u.Name, Scenario: u.Scenario, TZ: u.TZ}
		for _, tb := range goldenTables {
			*f.section(tb.section) = goldenRows(t, pool, user, tb.table, tb.order, tb.volatile, labels)
		}
		return f
	}
	first, last := goldenFirstDay, addDays(goldenFirstDay, goldenDays-1)
	score := func() {
		t.Helper()
		if err := engine.ComputeRange(ctx, user, first, last); err != nil {
			t.Fatal(err)
		}
	}
	same := func(what string, want, got goldenFile) {
		t.Helper()
		if c := goldenChanges(want, got, false); len(c) > 0 {
			t.Fatalf("%s: %s (before -> after):\n%s", what, goldenSummary(c), goldenReport(c))
		}
	}

	// Phase one: the month as synced, scored; scoring it again moves nothing
	// (a pass that read state an earlier pass of the same run had not yet
	// written would show here).
	score()
	once := read(t)
	score()
	f := read(t)
	same("scoring the month a second time moved stored values", once, f)

	// Phase two: what arrived after the month was scored, then the re-score a
	// sync triggers (here the whole range).
	if len(g.late) > 0 {
		goldenSync(t, st, user, g.loc, nil, nil, g.late)
		score()
		f = read(t)
	}

	// Newest first: the last goldenNewestFirst days scored again from nothing,
	// one call a day from the last (a backfill that arrives newest first);
	// ComputeDays re-scores the stored days after each new one, so the result
	// must be the same. Ten days hold both DST changes and the late workout;
	// the whole month would cost ~465 day passes a user.
	tail := addDays(first, goldenDays-goldenNewestFirst)
	if _, err := pool.Exec(ctx, `DELETE FROM daily_scores WHERE user_id = $1 AND day >= $2::date`, user, tail); err != nil {
		t.Fatal(err)
	}
	for k := goldenDays - 1; k >= goldenDays-goldenNewestFirst; k-- {
		if err := engine.ComputeDay(ctx, user, addDays(first, k)); err != nil {
			t.Fatal(err)
		}
	}
	same("the last days scored newest first differ from the range", f, read(t))
	return f, read
}

// goldenSync is ingest's sync after validate and screen (cmd/ingest, which
// this package cannot import; screen's window reads the wall clock): the
// upserts, the sources, and the apply with the request's same-source fold,
// whose scope is cmd/ingest/fold.go's foldRequest (the request's sources, its
// starts ±1 day). The apply gets no days: the harness scores the whole range
// itself (ComputeRange), so ingest's touched days are not needed and not
// copied here.
func goldenSync(t *testing.T, st *store.Store, user uuid.UUID, loc *time.Location, samples []model.Sample, sleep []model.SleepSegment, workouts []model.Workout) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.UpsertSamples(ctx, user, samples); err != nil {
		t.Fatalf("samples: %v", err)
	}
	if _, err := st.UpsertSleep(ctx, user, sleep); err != nil {
		t.Fatalf("sleep: %v", err)
	}
	if _, err := st.UpsertWorkouts(ctx, user, workouts); err != nil {
		t.Fatalf("workouts: %v", err)
	}
	if err := st.UpsertSources(ctx, user, store.SourcesOf(samples, sleep, workouts)); err != nil {
		t.Fatalf("sources: %v", err)
	}
	var fold *store.FoldScope
	for _, w := range workouts {
		if fold == nil {
			fold = &store.FoldScope{Sources: []string{}, From: w.Start, To: w.Start}
		}
		if !slices.Contains(fold.Sources, w.Source) {
			fold.Sources = append(fold.Sources, w.Source)
		}
		fold.From, fold.To = minTime(fold.From, w.Start), maxTime(fold.To, w.Start)
	}
	if fold != nil {
		fold.From, fold.To = fold.From.Add(-24*time.Hour), fold.To.Add(24*time.Hour)
	}
	if _, err := st.ApplySync(ctx, user, store.Apply{Loc: loc, Fold: fold}); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

// goldenRows reads the user's rows of a table whole but for the volatile
// columns, as Postgres renders them (UTC timestamps, shortest exact floats),
// ids replaced by their labels; an id without a label fails the test.
func goldenRows(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, table, order string, volatile []string, labels map[string]string) []map[string]any {
	t.Helper()
	ctx := context.Background()
	var raw []string
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		for _, set := range []string{"SET LOCAL TIME ZONE 'UTC'", "SET LOCAL extra_float_digits = 1"} {
			if _, err := tx.Exec(ctx, set); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT (to_jsonb(x) - $2::text[])::text FROM `+table+` x WHERE user_id = $1 ORDER BY `+order,
			user, volatile)
		if err != nil {
			return err
		}
		raw, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		var row map[string]any
		if err := goldenDecode([]byte(r), &row); err != nil {
			t.Fatal(err)
		}
		for _, c := range goldenIDColumns {
			id, ok := row[c].(string)
			if !ok {
				continue
			}
			l, ok := labels[id]
			if !ok {
				t.Fatalf("%s.%s: id %s has no label", table, c, id)
			}
			row[c] = l
		}
		out = append(out, row)
	}
	return out
}

// TestGoldenScores recomputes the users through the real Engine and diffs
// everything scoring stored against testdata/golden. RYFT_GOLDEN_UPDATE (make
// golden-update) rewrites the files instead, when the guard lets it.
func TestGoldenScores(t *testing.T) {
	mode := os.Getenv(goldenUpdate)
	update := mode != ""
	manifest, merr := readGoldenManifest()
	if merr != nil && !errors.Is(merr, fs.ErrNotExist) {
		t.Fatalf("manifest: %v", merr)
	}
	var have *goldenManifest
	if merr == nil {
		have = &manifest
	}
	admin := os.Getenv("RYFT_TEST_POSTGRES_URL")
	if admin == "" {
		if update || os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("the golden scores need RYFT_TEST_POSTGRES_URL (a throwaway Postgres server); in CI they never skip")
		}
		t.Skip("set RYFT_TEST_POSTGRES_URL to a throwaway Postgres server to run the golden scores")
	}
	if update {
		head, disk, err := goldenHEADManifest()
		if err == nil {
			err = goldenCommitted(head, disk)
		}
		if err != nil {
			t.Fatalf("refusing to regenerate the golden files: %v", err)
		}
	} else if have == nil {
		t.Fatal("no testdata/golden/manifest.json: generate the files with make golden-update")
	}

	st, pool, engine := engineDB(t, admin)
	st.WithImportRules(store.ImportRules{SameSession: train.SameSession, Conflict: train.ImportConflict,
		SamePlan: train.SamePlan, PlanConflict: train.PlanConflict}) // as the api wires them
	lib := library(t)
	inputs := map[string]goldenInputs{}
	got := map[string]goldenFile{}
	reread := map[string]func(*testing.T) goldenFile{}
	for _, u := range goldenUsers {
		t.Run(u.Name, func(t *testing.T) {
			g, err := newGoldenGen(u, lib)
			if err != nil {
				t.Fatal(err)
			}
			inputs[u.Name] = g.inputs(u)
			f, read := goldenRun(t, st, pool, engine, u, g)
			if len(f.Days) != goldenDays {
				t.Fatalf("%d stored days, want %d", len(f.Days), goldenDays)
			}
			got[u.Name], reread[u.Name] = f, read
			if update {
				return
			}
			want, err := readGoldenFile(u.Name)
			if err != nil {
				t.Fatalf("golden file: %v", err)
			}
			versionMoved := manifest.ModelVersion != ModelVersion
			changes := goldenChanges(want, f, versionMoved)
			var hint []string
			if versionMoved {
				hint = append(hint, fmt.Sprintf("ModelVersion is %s, the files are %s (model_version on every row not listed): review this diff, then make golden-update",
					ModelVersion, manifest.ModelVersion))
			}
			if len(changes) == 0 {
				if versionMoved {
					t.Errorf("%s: nothing but model_version moved; %s", u.Name, hint[0])
				}
				return
			}
			if was, ok := manifest.Inputs[u.Name]; ok && was.Digest != inputs[u.Name].Digest {
				hint = append(hint, fmt.Sprintf("the generator's inputs changed (%+v, the manifest has %+v): the diff is the generator's, not necessarily the model's; make golden-update regenerates the users whose inputs changed",
					inputs[u.Name], was))
			}
			switch {
			case !slices.ContainsFunc(changes, func(c goldenChange) bool { return !c.addition() }):
				hint = append(hint, "only new fields or rows appeared, no stored value moved: a new column? make golden-update GOLDEN_MODE=additive")
			case !versionMoved:
				hint = append(hint, "a stored number moved: a bug, or a model change that needs a ModelVersion bump, a changelog entry, docs/SCORING.md and then make golden-update")
			}
			t.Errorf("%s (%s): %s (golden -> now):\n%s%s", u.Name, u.Scenario, goldenSummary(changes),
				goldenReport(changes), strings.Join(hint, "\n"))
		})
	}
	// Every user's rows as they were when that user finished: a later user's
	// scoring must not have written them (each statement scoped to its user).
	for _, u := range goldenUsers {
		if read, ok := reread[u.Name]; ok {
			if c := goldenChanges(got[u.Name], read(t), false); len(c) > 0 {
				t.Errorf("%s's rows changed while the users after it were scored: %s (then -> now):\n%s",
					u.Name, goldenSummary(c), goldenReport(c))
			}
		}
	}
	if !update || t.Failed() {
		return
	}
	if err := goldenAllowUpdate(mode, have, goldenDirHasFiles(), got, inputs); err != nil {
		t.Fatalf("refusing to regenerate the golden files: %v", err)
	}
	if err := os.MkdirAll(goldenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(goldenUsers))
	for k, u := range goldenUsers {
		names[k] = u.Name
		writeGoldenJSON(t, goldenPath(u.Name), got[u.Name])
	}
	excluded := map[string][]string{}
	for _, tb := range goldenTables {
		excluded[tb.table] = tb.volatile
	}
	writeGoldenJSON(t, filepath.Join(goldenDir, "manifest.json"), goldenManifest{
		ModelVersion: ModelVersion, FirstDay: goldenFirstDay, Days: goldenDays, Seed: goldenSeed,
		Users: names, Inputs: inputs, Fields: goldenFields(got), Excluded: excluded,
		Numbers: "rounded to 1e-6, printed shortest; ids replaced by labels; digests: first 64 bits of a SHA-256, " +
			"inputs over the generator's canonical field=value lines, fields over the files' field names",
		Regenerate: "make golden-update: after a ModelVersion bump, or for the users whose generator inputs changed; " +
			"GOLDEN_MODE=additive for new fields only, GOLDEN_MODE=init for the first set; commit before the next update",
	})
	t.Logf("golden files written for ModelVersion %s (%s): review git diff %s before committing", ModelVersion, mode, goldenDir)
}
