package scoring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// Golden files: the safety net for refactors (docs/SCORING.md → Model version).
// The generator's users (golden_gen_test.go) go through the store calls ingest
// and the api make (a sync a day with its apply and fold, sessions saved and
// completed or imported) and the real Engine (ComputeRange, as
// scripts/rescore_all.sh does). Then everything scoring writes must equal
// testdata/golden/<user>.json: every daily_scores column, and the workouts
// (strain, trimp, zones, duplicate_of, the attached session and its load, the
// ones scoring creates for sessions) and sessions (load and workout) rows, ids
// replaced by readable labels. Nothing here is worked out by hand: the
// calibration tests pin what a number should be, this pins that it did not move.
//
// A diff means a stored number moved: a bug, or a model change, which bumps
// ModelVersion (version.go, changelog.json, SCORING.md) and then regenerates
// with `make golden-update`. The update refuses while ModelVersion is the
// manifest's, unless the generator's inputs changed (their digest), and then
// only for the users whose inputs changed; GOLDEN_MODE=additive accepts new
// fields and rows and refuses any changed one.
//
// Determinism: the generator's, plus the read in UTC with Postgres's shortest
// exact floats, and canonical JSON (sorted keys, numbers rounded to 1e-6 and
// printed shortest, which also absorbs last-bit differences between CPUs).
//
// Opt-in like every engine test (RYFT_TEST_POSTGRES_URL), but never skipped in
// CI. TestGoldenManifest needs no database and always runs.

const (
	goldenDir      = "testdata/golden"
	goldenUpdate   = "RYFT_GOLDEN_UPDATE" // 1: regenerate (make golden-update); additive: new fields and rows only
	goldenMaxLines = 60                   // report lines per user
)

// The tables pinned, in report order, and the columns left out of each: the
// row's owner (a generated id) and wall-clock stamps of the write.
var goldenTables = []struct {
	section, table, key, order string
	volatile                   []string
}{
	{"days", "daily_scores", "day", "day", []string{"user_id", "computed_at"}},
	{"workouts", "workouts", "id", "start_at, source, id", []string{"user_id", "created_at"}},
	{"sessions", "sessions", "id", "started_at, id", []string{"user_id", "created_at", "updated_at", "imported_at"}},
}

// goldenIDColumns hold ids; the files have labels in their place.
var goldenIDColumns = []string{"id", "session_id", "duplicate_of", "workout_id", "plan_day_id", "device_id"}

// goldenFile is one user's file: the rows of each table, oldest first.
type goldenFile struct {
	User     string           `json:"user"`
	Scenario string           `json:"scenario"`
	TZ       string           `json:"tz"`
	Days     []map[string]any `json:"days"`
	Workouts []map[string]any `json:"workouts"`
	Sessions []map[string]any `json:"sessions"`
}

func (f *goldenFile) section(name string) *[]map[string]any {
	switch name {
	case "days":
		return &f.Days
	case "workouts":
		return &f.Workouts
	}
	return &f.Sessions
}

type goldenManifest struct {
	ModelVersion string                  `json:"model_version"`
	FirstDay     string                  `json:"first_day"`
	Days         int                     `json:"days"`
	Seed         int                     `json:"seed"`
	Users        []string                `json:"users"`
	Inputs       map[string]goldenInputs `json:"inputs"`
	Excluded     map[string][]string     `json:"excluded_columns"`
	Numbers      string                  `json:"numbers"`
	Regenerate   string                  `json:"regenerate"`
}

// ---- running a user ----------------------------------------------------------------

// goldenRun stores the user's month the way ingest and the api do and scores
// it, returning the user's file.
func goldenRun(t *testing.T, st *store.Store, pool *pgxpool.Pool, engine *Engine, u goldenUser, g *goldenGen) goldenFile {
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

	if err := engine.ComputeRange(ctx, user, goldenFirstDay, addDays(goldenFirstDay, goldenDays-1)); err != nil {
		t.Fatal(err)
	}

	labels := map[string]string{}
	label := func(id uuid.UUID, l string) {
		if old, ok := labels[id.String()]; ok && old != l {
			t.Fatalf("golden: %s and %s share an id", old, l)
		}
		labels[id.String()] = l
	}
	for _, w := range g.workouts {
		label(w.ID, "workout "+w.Source+" "+w.Start.In(g.loc).Format("2006-01-02T15:04"))
	}
	for _, w := range g.resends {
		label(w.ID, "workout "+w.Source+" "+w.Start.In(g.loc).Format("2006-01-02T15:04")+" resent")
	}
	for _, gs := range g.sessions {
		label(gs.Session.ID, "session "+gs.Session.Title)
		label(train.SessionWorkoutID(user, gs.Session.ID), "workout created for session "+gs.Session.Title)
	}
	f := goldenFile{User: u.Name, Scenario: u.Scenario, TZ: u.TZ}
	for _, tb := range goldenTables {
		*f.section(tb.section) = goldenRows(t, pool, user, tb.table, tb.order, tb.volatile, labels)
	}
	return f
}

// goldenSync is ingest's sync after validate and screen (cmd/ingest, which
// this package cannot import; screen's window reads the wall clock): the
// upserts, the sources, and the apply with the request's days and its
// same-source fold (cmd/ingest/fold.go's scope: the request's sources, its
// starts ±1 day).
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
	days := map[string]bool{}
	for _, s := range samples {
		days[s.Start.In(loc).Format(dayLayout)] = true
	}
	for _, s := range sleep {
		days[s.End.In(loc).Format(dayLayout)] = true
	}
	var fold *store.FoldScope
	for _, w := range workouts {
		days[w.Start.In(loc).Format(dayLayout)] = true
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
	if _, err := st.ApplySync(ctx, user, store.Apply{Days: slices.Sorted(maps.Keys(days)), Loc: loc, Fold: fold}); err != nil {
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

// ---- canonical JSON ----------------------------------------------------------------

func goldenDecode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(v)
}

// goldenNumber is a number as the files hold it: rounded to 1e-6 and printed
// shortest, so 12, 12.5 and 0.333333; never -0.
func goldenNumber(n json.Number) string {
	f, err := n.Float64()
	if err != nil {
		return n.String()
	}
	f = math.Round(f*1e6) / 1e6
	if f == 0 {
		f = 0
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func goldenString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func goldenScalar(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}

// goldenWrite renders v canonically: keys sorted, numbers as goldenNumber,
// pretty with one key per line (arrays of scalars on one line) or compact.
func goldenWrite(b *strings.Builder, v any, indent string, pretty bool) {
	sep, open, pad := ",", "", ""
	if pretty {
		sep, open, pad = ",\n", "\n", indent+"  "
	}
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{" + open)
		for k, key := range slices.Sorted(maps.Keys(x)) {
			if k > 0 {
				b.WriteString(sep)
			}
			b.WriteString(pad + goldenString(key) + ":")
			if pretty {
				b.WriteString(" ")
			}
			goldenWrite(b, x[key], pad, pretty)
		}
		if pretty {
			b.WriteString("\n" + indent)
		}
		b.WriteString("}")
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		inline := !pretty || !slices.ContainsFunc(x, func(e any) bool { return !goldenScalar(e) })
		b.WriteString("[")
		for k, e := range x {
			switch {
			case k > 0 && inline:
				b.WriteString(", ")
			case k > 0:
				b.WriteString(",")
			}
			if !inline {
				b.WriteString("\n" + pad)
			}
			goldenWrite(b, e, pad, pretty)
		}
		if !inline {
			b.WriteString("\n" + indent)
		}
		b.WriteString("]")
	case json.Number:
		b.WriteString(goldenNumber(x))
	case string:
		b.WriteString(goldenString(x))
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case nil:
		b.WriteString("null")
	default:
		panic(fmt.Sprintf("golden: unexpected %T", v))
	}
}

func goldenText(v any) string {
	var b strings.Builder
	goldenWrite(&b, v, "", true)
	return b.String() + "\n"
}

// goldenFlatten maps a row to its leaves: "details.recovery.hrv_z" → "0.41";
// arrays are one leaf (zone_minutes reads as one value).
func goldenFlatten(prefix string, v any, out map[string]string) {
	if m, ok := v.(map[string]any); ok && len(m) > 0 {
		for k, e := range m {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			goldenFlatten(p, e, out)
		}
		return
	}
	var b strings.Builder
	goldenWrite(&b, v, "", false)
	out[prefix] = b.String()
}

func goldenPath(name string) string { return filepath.Join(goldenDir, name+".json") }

func readGoldenFile(name string) (goldenFile, error) {
	var f goldenFile
	raw, err := os.ReadFile(goldenPath(name))
	if err != nil {
		return f, err
	}
	return f, goldenDecode(raw, &f)
}

func writeGoldenJSON(t *testing.T, path string, v any) {
	t.Helper()
	// Through the decoder, so every number is a json.Number and printed as goldenNumber.
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := goldenDecode(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(goldenText(generic)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readGoldenManifest() (goldenManifest, error) {
	var m goldenManifest
	raw, err := os.ReadFile(filepath.Join(goldenDir, "manifest.json"))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(raw, &m)
}

// ---- the diff --------------------------------------------------------------------

// goldenChange is one leaf that differs: changed, added (from is absent) or
// removed (to is absent). A whole row added or removed has field "(row)".
type goldenChange struct {
	section, row, field, from, to string
}

const goldenAbsent = "(absent)"

func (c goldenChange) addition() bool { return c.from == goldenAbsent }

// goldenChanges compares two files leaf by leaf, rows matched by key (the day,
// the label). skipVersion drops the model_version leaves that moved to
// ModelVersion: after a bump every row has one, and the report says so once.
func goldenChanges(want, got goldenFile, skipVersion bool) []goldenChange {
	var out []goldenChange
	for _, tb := range goldenTables {
		index := func(rows []map[string]any) map[string]map[string]string {
			m := map[string]map[string]string{}
			for _, r := range rows {
				key, _ := r[tb.key].(string)
				flat := map[string]string{}
				goldenFlatten("", r, flat)
				m[key] = flat
			}
			return m
		}
		w, g := index(*want.section(tb.section)), index(*got.section(tb.section))
		keys := slices.Sorted(maps.Keys(w))
		for k := range g {
			if _, ok := w[k]; !ok {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, key := range keys {
			wr, wok := w[key]
			gr, gok := g[key]
			switch {
			case !gok:
				out = append(out, goldenChange{tb.section, key, "(row)", "stored", goldenAbsent})
				continue
			case !wok:
				out = append(out, goldenChange{tb.section, key, "(row)", goldenAbsent, "stored"})
				continue
			}
			fields := slices.Sorted(maps.Keys(wr))
			for f := range gr {
				if _, ok := wr[f]; !ok {
					fields = append(fields, f)
				}
			}
			slices.Sort(fields)
			for _, f := range fields {
				a, aok := wr[f]
				b, bok := gr[f]
				if !aok {
					a = goldenAbsent
				}
				if !bok {
					b = goldenAbsent
				}
				if a == b || (skipVersion && f == "model_version" && b == goldenString(ModelVersion)) {
					continue
				}
				out = append(out, goldenChange{tb.section, key, f, a, b})
			}
		}
	}
	return out
}

// goldenReport reads the changes grouped by field: a field that moved the
// same way on several rows is one line, otherwise one line per row; at most
// goldenMaxLines lines, then a count.
func goldenReport(changes []goldenChange) string {
	type group struct {
		name string
		cs   []goldenChange
	}
	var groups []*group
	byName := map[string]*group{}
	for _, c := range changes {
		name := c.section + "." + c.field
		if byName[name] == nil {
			byName[name] = &group{name: name}
			groups = append(groups, byName[name])
		}
		byName[name].cs = append(byName[name].cs, c)
	}
	var lines []string
	for _, g := range groups {
		same := !slices.ContainsFunc(g.cs, func(c goldenChange) bool { return c.from != g.cs[0].from || c.to != g.cs[0].to })
		if same {
			rows := make([]string, len(g.cs))
			for k, c := range g.cs {
				rows[k] = c.row
			}
			where := strings.Join(rows, ", ")
			if len(rows) > 4 {
				where = fmt.Sprintf("%s .. %s", rows[0], rows[len(rows)-1])
			}
			lines = append(lines, fmt.Sprintf("  %s: %s -> %s on %d row(s): %s", g.name, g.cs[0].from, g.cs[0].to, len(rows), where))
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s: %d row(s)", g.name, len(g.cs)))
		for _, c := range g.cs {
			lines = append(lines, fmt.Sprintf("      %s  %s -> %s", c.row, c.from, c.to))
		}
	}
	var b strings.Builder
	for k, l := range lines {
		if k == goldenMaxLines {
			fmt.Fprintf(&b, "  ... and %d more lines\n", len(lines)-k)
			break
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}

// goldenSummary counts what differs: days, workouts, sessions.
func goldenSummary(changes []goldenChange) string {
	rows := map[string]map[string]bool{}
	for _, c := range changes {
		if rows[c.section] == nil {
			rows[c.section] = map[string]bool{}
		}
		rows[c.section][c.row] = true
	}
	var parts []string
	for _, tb := range goldenTables {
		if n := len(rows[tb.section]); n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", tb.section, n))
		}
	}
	return "rows differ: " + strings.Join(parts, ", ")
}

// ---- the update guard --------------------------------------------------------------

// goldenAllowUpdate says whether the files may be regenerated (mode "1") or
// extended (mode "additive") with what the run computed; nil if so.
func goldenAllowUpdate(mode string, m *goldenManifest, dirHasFiles bool, got map[string]goldenFile, inputs map[string]goldenInputs) error {
	if mode != "1" && mode != "additive" {
		return fmt.Errorf("%s=%q: use 1 or additive", goldenUpdate, mode)
	}
	if m == nil {
		if dirHasFiles {
			return errors.New("golden files without manifest.json: restore it from git rather than regenerate blind")
		}
		return nil // the first generation
	}
	onlyAdditions := func(name string) error {
		want, err := readGoldenFile(name)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // a new user
		}
		if err != nil {
			return err
		}
		for _, c := range goldenChanges(want, got[name], false) {
			if !c.addition() {
				return fmt.Errorf("%s: %s %s.%s moved (%s -> %s)", name, c.row, c.section, c.field, c.from, c.to)
			}
		}
		return nil
	}
	for _, name := range m.Users {
		if _, ok := got[name]; !ok {
			return fmt.Errorf("%s is in the manifest but no longer generated", name)
		}
	}
	if mode == "additive" {
		for _, name := range slices.Sorted(maps.Keys(got)) {
			if err := onlyAdditions(name); err != nil {
				return fmt.Errorf("additive mode refuses: %w", err)
			}
		}
		return nil
	}
	if m.ModelVersion != ModelVersion {
		return nil
	}
	var generator []string
	for name, in := range inputs {
		if was, ok := m.Inputs[name]; !ok || was.Digest != in.Digest {
			generator = append(generator, name)
		}
	}
	if len(generator) == 0 {
		return fmt.Errorf("ModelVersion is still %s, the version the files were made with, and the generator's inputs are unchanged. "+
			"A change that moves a stored number bumps ModelVersion first (version.go, changelog.json, docs/SCORING.md); "+
			"a change that should not move one must pass as it is; new fields only: GOLDEN_MODE=additive", ModelVersion)
	}
	for _, name := range slices.Sorted(maps.Keys(got)) {
		if slices.Contains(generator, name) {
			continue
		}
		if err := onlyAdditions(name); err != nil {
			return fmt.Errorf("the generator changed only %v, but %w: that is a model change", generator, err)
		}
	}
	return nil
}

func goldenDirHasFiles() bool {
	files, _ := filepath.Glob(filepath.Join(goldenDir, "*.json"))
	return len(files) > 0
}

// ---- the tests ---------------------------------------------------------------------

// TestGoldenScores recomputes the users through the real Engine and diffs
// everything scoring stored against testdata/golden. RYFT_GOLDEN_UPDATE (make
// golden-update) rewrites the files instead, when goldenAllowUpdate lets it.
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
	if update && have != nil && mode == "1" && manifest.ModelVersion == ModelVersion {
		t.Logf("ModelVersion is the manifest's: regenerating only if the generator's inputs changed")
	}
	admin := os.Getenv("RYFT_TEST_POSTGRES_URL")
	if admin == "" {
		if update || os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("the golden scores need RYFT_TEST_POSTGRES_URL (a throwaway Postgres server); in CI they never skip")
		}
		t.Skip("set RYFT_TEST_POSTGRES_URL to a throwaway Postgres server to run the golden scores")
	}
	if !update && have == nil {
		t.Fatal("no testdata/golden/manifest.json: generate the files with make golden-update")
	}

	st, pool, engine := engineDB(t, admin)
	st.WithImportRules(store.ImportRules{SameSession: train.SameSession, Conflict: train.ImportConflict,
		SamePlan: train.SamePlan, PlanConflict: train.PlanConflict}) // as the api wires them
	lib := library(t)
	inputs := map[string]goldenInputs{}
	got := map[string]goldenFile{}
	for _, u := range goldenUsers {
		t.Run(u.Name, func(t *testing.T) {
			g, err := newGoldenGen(u, lib)
			if err != nil {
				t.Fatal(err)
			}
			inputs[u.Name] = g.inputs(t, u)
			f := goldenRun(t, st, pool, engine, u, g)
			if len(f.Days) != goldenDays {
				t.Fatalf("%d stored days, want %d", len(f.Days), goldenDays)
			}
			got[u.Name] = f
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
	if !update || t.Failed() {
		return
	}
	if err := goldenAllowUpdate(mode, have, goldenDirHasFiles(), got, inputs); err != nil {
		t.Fatalf("refusing to regenerate the golden files: %v", err)
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
		Users: names, Inputs: inputs, Excluded: excluded,
		Numbers:    "rounded to 1e-6, printed shortest; ids replaced by labels; digest: first 64 bits of the inputs' SHA-256",
		Regenerate: "make golden-update: after a ModelVersion bump, or for the users whose generator inputs changed; GOLDEN_MODE=additive for new fields only",
	})
	t.Logf("golden files written for ModelVersion %s (%s): review git diff %s before committing", ModelVersion, mode, goldenDir)
}

// TestGoldenManifest needs no database: the files must belong to the current
// ModelVersion (a bump without make golden-update fails here), list every
// user of the generator, and hold 30 consecutive days each, scored by that version.
func TestGoldenManifest(t *testing.T) {
	if os.Getenv(goldenUpdate) != "" {
		t.Skip("regenerating")
	}
	m, err := readGoldenManifest()
	if err != nil {
		t.Fatalf("manifest: %v (generate the files with make golden-update)", err)
	}
	if m.ModelVersion != ModelVersion {
		t.Fatalf("ModelVersion is %s, the golden files were made with %s: regenerate them with make golden-update "+
			"(RYFT_TEST_POSTGRES_URL) and commit the reviewed diff with the bump", ModelVersion, m.ModelVersion)
	}
	if m.FirstDay != goldenFirstDay || m.Days != goldenDays || m.Seed != goldenSeed {
		t.Errorf("manifest %s × %d, seed %d; the generator %s × %d, seed %d", m.FirstDay, m.Days, m.Seed,
			goldenFirstDay, goldenDays, goldenSeed)
	}
	var names []string
	for _, u := range goldenUsers {
		names = append(names, u.Name)
	}
	if !slices.Equal(m.Users, names) {
		t.Errorf("manifest users %v, the generator's %v", m.Users, names)
	}
	for _, name := range m.Users {
		f, err := readGoldenFile(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if f.User != name || len(f.Days) != m.Days {
			t.Errorf("%s: user %q with %d days, want %d", name, f.User, len(f.Days), m.Days)
			continue
		}
		for k, r := range f.Days {
			if day := addDays(m.FirstDay, k); r["day"] != day || r["model_version"] != m.ModelVersion {
				t.Errorf("%s day %d: %v scored by %v, want %s by %s", name, k, r["day"], r["model_version"], day, m.ModelVersion)
			}
		}
		for _, w := range f.Workouts {
			if v, ok := w["model_version"].(string); ok && v != m.ModelVersion {
				t.Errorf("%s %v: model_version %s", name, w["id"], v)
			}
		}
	}
}

// TestGoldenDiffReads pins the failure message's shape and the update guard's
// rules on made-up changes.
func TestGoldenDiffReads(t *testing.T) {
	day := func(d, rec, z string) map[string]any {
		return map[string]any{"day": d, "recovery": json.Number(rec), "model_version": ModelVersion,
			"details": map[string]any{"recovery": map[string]any{"hrv_z": json.Number(z)}}, "zone_minutes": []any{json.Number("1"), json.Number("2")}}
	}
	want := goldenFile{Days: []map[string]any{day("2026-10-06", "64", "0.41"), day("2026-10-07", "50", "0.2")}}
	got := goldenFile{Days: []map[string]any{day("2026-10-06", "61", "0.3800000001"), day("2026-10-07", "47", "0.2")}}
	changes := goldenChanges(want, got, false)
	report := goldenReport(changes)
	wantReport := "  days.details.recovery.hrv_z: 0.41 -> 0.38 on 1 row(s): 2026-10-06\n" +
		"  days.recovery: 2 row(s)\n" +
		"      2026-10-06  64 -> 61\n" +
		"      2026-10-07  50 -> 47\n"
	if report != wantReport || goldenSummary(changes) != "rows differ: days 2" {
		t.Fatalf("report:\n%s\nwant\n%s(%s)", report, wantReport, goldenSummary(changes))
	}
	if c := goldenChanges(want, want, false); len(c) != 0 {
		t.Fatalf("no change: %v", c)
	}
	same := goldenFile{Days: []map[string]any{day("2026-10-06", "64", "0.4100000001"), day("2026-10-07", "50", "0.2")}}
	if c := goldenChanges(want, same, false); len(c) != 0 {
		t.Fatalf("a difference below 1e-6 is none: %v", c)
	}
	// A new column is an addition, nothing else; a bumped model_version is skipped when asked.
	added := goldenFile{Days: []map[string]any{day("2026-10-06", "64", "0.41"), day("2026-10-07", "50", "0.2")}}
	added.Days[0]["new_col"] = json.Number("1")
	if c := goldenChanges(want, added, false); len(c) != 1 || !c[0].addition() {
		t.Fatalf("an added column: %+v", c)
	}
	bumped := goldenFile{Days: []map[string]any{day("2026-10-06", "64", "0.41"), day("2026-10-07", "50", "0.2")}}
	want.Days[0]["model_version"] = "2026.01.1"
	if c := goldenChanges(want, bumped, true); len(c) != 0 {
		t.Fatalf("model_version after a bump: %+v", c)
	}
	if got := goldenText(map[string]any{"b": json.Number("-0.0000001"), "a": []any{json.Number("1.50"), "x"}}); got != "{\n  \"a\": [1.5, \"x\"],\n  \"b\": 0\n}\n" {
		t.Fatalf("canonical text %q", got)
	}

	// The guard: a missing manifest with files refuses; same version and inputs refuse.
	if err := goldenAllowUpdate("1", nil, true, nil, nil); err == nil {
		t.Fatal("files without a manifest must refuse")
	}
	if err := goldenAllowUpdate("1", nil, false, nil, nil); err != nil {
		t.Fatalf("the first generation: %v", err)
	}
	m := &goldenManifest{ModelVersion: ModelVersion, Inputs: map[string]goldenInputs{"user-x": {Digest: "1"}}}
	in := map[string]goldenInputs{"user-x": {Digest: "1"}}
	if err := goldenAllowUpdate("1", m, true, map[string]goldenFile{"user-x": {}}, in); err == nil {
		t.Fatal("same version, same inputs must refuse")
	}
	if err := goldenAllowUpdate("1", m, true, map[string]goldenFile{"user-x": {}}, map[string]goldenInputs{"user-x": {Digest: "2"}}); err != nil {
		t.Fatalf("changed inputs: %v", err)
	}
	if err := goldenAllowUpdate("yes", m, true, nil, nil); err == nil {
		t.Fatal("an unknown mode must refuse")
	}
}
